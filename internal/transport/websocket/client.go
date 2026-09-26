package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/safego"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
)

const (
	writeWait = 10 * time.Second

	pongWait = 60 * time.Second

	pingPeriod = (pongWait * 9) / 10

	maxMessageSize = 4096
)

// newUpgrader строит WebSocket upgrader с проверкой Origin по allowlist.
// Пустой allowedOrigins => разрешены любые источники (нативные клиенты,
// обратная совместимость). Запросы без заголовка Origin (мобильные приложения)
// пропускаются всегда.
func newUpgrader(allowedOrigins []string) websocket.Upgrader {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		if o = strings.TrimSpace(o); o != "" {
			allowed[o] = true
		}
	}

	return websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			if len(allowed) == 0 {
				return true
			}
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true
			}
			return allowed[origin]
		},
	}
}

type Client struct {
	userID      int64
	connID      string
	displayName string
	hub         *Hub
	conn        *websocket.Conn
	send        chan []byte
	chatService *service.ChatService
	presence    *service.PresenceService
	log         *slog.Logger
	rooms       []int64
	// work carries decoded inbound events from readPump to a single per-connection
	// worker (eventPump). readPump is its only sender, so it is safe to close on
	// teardown. A bounded buffer + non-blocking enqueue gives backpressure instead
	// of the old unbounded "go func per frame", and the single worker preserves
	// per-connection message order (two messages sent A then B persist A then B).
	work chan WSEvent
	// cancel tears this connection down. The hub calls it (on unregister or
	// shutdown) instead of closing c.send: c.send has multiple senders (hub
	// broadcasts, sendError, presence snapshot), and closing a channel other
	// goroutines still send to would panic. Cancelling the ctx makes writePump
	// exit and close the socket, which unblocks readPump.
	cancel context.CancelFunc
}

const workBuffer = 32

// stop tears the connection down idempotently. Safe to call from the hub
// goroutine; nil-safe so hub tests can build a Client without a real ctx.
func (c *Client) stop() {
	if c.cancel != nil {
		c.cancel()
	}
}

func (c *Client) readPump(ctx context.Context, cancel context.CancelFunc) {
	defer func() {
		cancel()
		// readPump is the sole sender to work, so closing it here is safe and it
		// signals eventPump to drain-and-exit.
		close(c.work)
		// Unregister without blocking on hub shutdown, THEN run presence cleanup —
		// so a graceful restart still stamps last_seen / emits userOffline instead
		// of wedging on a channel whose reader (hub.Run) has already returned.
		c.unregisterFromHub()
		_ = c.conn.Close() // второй Close (из writePump) вернёт ошибку — это штатно
		c.markOffline()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	// Ошибка дедлайна значит, что сокет уже мёртв — ReadMessage ниже её увидит.
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	// Ошибку из pong-обработчика ReadMessage возвращает как свою, и readPump
	// штатно сворачивает соединение, а не ждёт истечения старого дедлайна.
	c.conn.SetPongHandler(func(string) error { return c.conn.SetReadDeadline(time.Now().Add(pongWait)) })

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			var closeErr *websocket.CloseError
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure, websocket.CloseNormalClosure) {
				c.log.Error("websocket unexpected close", slog.Any("err", err))
			} else if !errors.As(err, &closeErr) {
				// Log non-close errors (e.g., protocol violations, unmasked frames, invalid utf-8, read timeouts)
				c.log.Error("websocket read error", slog.Any("err", err))
			}
			break
		}

		var event WSEvent
		if err := json.Unmarshal(message, &event); err != nil {
			c.log.Error("failed to unmarshal WS event", slog.Any("err", err))
			continue
		}

		// Hand the event to the single worker instead of spawning a goroutine per
		// frame. If the worker is backed up (buffer full), tell the client to slow
		// down rather than growing goroutines/DB work without bound.
		select {
		case c.work <- event:
		default:
			c.sendError(ctx, event.RequestID, "you are sending events too fast")
		}
	}
}

// unregisterFromHub removes the client from the hub, without blocking if the hub
// has already shut down (its Run loop returned and closed done, so nothing reads
// unregister anymore).
func (c *Client) unregisterFromHub() {
	select {
	case c.hub.unregister <- c:
	case <-c.hub.done:
	}
}

// eventPump is the single per-connection worker: it processes decoded events in
// order, one at a time. Each event is handled under its own recover so a panic
// in one message (e.g. a nil deref in the DTO mapper) is contained to that
// message instead of killing the worker or, worse, the whole process.
func (c *Client) eventPump(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-c.work:
			if !ok {
				return
			}
			c.handleEventGuarded(ctx, event)
		}
	}
}

func (c *Client) handleEventGuarded(ctx context.Context, event WSEvent) {
	defer safego.Recover(c.log, "ws event handler")
	c.handleEvent(ctx, event)
}

func (c *Client) handleEvent(ctx context.Context, event WSEvent) {
	switch event.Type {
	case EventNewMessage:
		var req WSSendMessagePayload
		if err := json.Unmarshal(event.Payload, &req); err != nil {
			c.sendError(ctx, event.RequestID, "invalid payload for newMessage")
			return
		}

		// request_id живёт на конверте события и служит idempotency-ключом:
		// повторная отправка того же события не создаст дубль (см. SaveMessage).
		var reqID *string
		if event.RequestID != "" {
			id := event.RequestID
			reqID = &id
		}

		// Обрабатываем синхронно в горутине eventPump: порядок сообщений одного
		// соединения сохраняется, а число одновременных обращений к БД ограничено
		// одним воркером на соединение (backpressure — в readPump).
		timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		resp, targetIDs, created, err := c.chatService.SendMessage(timeoutCtx, req.ChatID, c.userID, req.Content, req.ReplyToMessageID, req.FileID, reqID)
		if err != nil {
			c.log.Error("failed to send message via WS", slog.Any("err", err))
			c.sendError(ctx, event.RequestID, wsErrorMessage(err))
			return
		}

		respPayload, err := json.Marshal(resp)
		if err != nil {
			c.log.Error("failed to marshal response via WS", slog.Any("err", err))
			c.sendError(ctx, event.RequestID, "internal server error")
			return
		}

		responseEvent := WSEvent{
			Type:      EventNewMessage,
			RequestID: event.RequestID,
			Payload:   respPayload,
		}

		finalData, _ := json.Marshal(responseEvent)
		// Новое сообщение — всем участникам; идемпотентный повтор — только
		// отправителю (его optimistic-UI ждёт подтверждения, а остальные
		// уже получили сообщение при первой отправке).
		if created {
			c.hub.BroadcastToUsers(targetIDs, finalData)
		} else {
			c.hub.BroadcastToUsers([]int64{c.userID}, finalData)
		}

	case EventUserTyping:
		var req WSTypingPayload
		if err := json.Unmarshal(event.Payload, &req); err != nil {
			return
		}

		// Печатать можно только в чат, на который клиент подписан при
		// подключении — иначе клиент мог бы вбросить "X печатает…" в чужую
		// комнату по произвольному chat_id.
		if !slices.Contains(c.rooms, req.ChatID) {
			return
		}

		req.UserID = c.userID
		req.DisplayName = c.displayName
		newPayload, _ := json.Marshal(req)

		responseEvent := WSEvent{
			Type:      EventUserTyping,
			RequestID: event.RequestID,
			Payload:   newPayload,
		}

		finalData, _ := json.Marshal(responseEvent)
		c.hub.BroadcastToRooms(req.ChatID, finalData, c.userID)

	case EventMessagesRead:
		var req WSMessagesReadPayload
		if err := json.Unmarshal(event.Payload, &req); err != nil {
			c.sendError(ctx, event.RequestID, "invalid payload for messagesRead")
			return
		}

		timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		targetIDs, err := c.chatService.MarkAsRead(timeoutCtx, req.ChatID, c.userID, req.LastReadMessageID)
		if err != nil {
			c.log.Error("failed to mark messages as read via WS", slog.Any("err", err))
			c.sendError(ctx, event.RequestID, wsErrorMessage(err))
			return
		}

		// Рассылаем серверный payload, а не отражаем клиентский: иначе
		// произвольные/спуфнутые поля ушли бы участникам чата.
		readPayload, _ := json.Marshal(WSMessagesReadPayload{
			ChatID:            req.ChatID,
			LastReadMessageID: req.LastReadMessageID,
		})
		responseEvent := WSEvent{
			Type:      EventMessagesRead,
			RequestID: event.RequestID,
			Payload:   readPayload,
		}

		finalData, _ := json.Marshal(responseEvent)
		c.hub.BroadcastToUsers(targetIDs, finalData)

	default:
		c.log.Warn("unknown WS event type", slog.String("type", event.Type))
		c.sendError(ctx, event.RequestID, "unknown event type")
	}
}

// markOffline reports the lost connection to presence and, if it was the user's
// last connection anywhere, broadcasts userOffline. Idempotent per connID, so a
// backpressure-drop and the readPump exit can both run without double-firing. It
// uses a fresh context: the connection ctx is already cancelled when this runs,
// which would otherwise abort the SREM/last_seen writes.
func (c *Client) markOffline() {
	if c.presence == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	offline, lastSeen, recipients, err := c.presence.OnDisconnect(ctx, c.userID, c.connID)
	if err != nil {
		c.log.Error("presence: on disconnect", slog.Any("err", err))
		return
	}
	if offline && len(recipients) > 0 {
		ls := lastSeen.Unix()
		c.hub.BroadcastToUsers(recipients, presenceEvent(EventUserOffline, c.userID, false, c.displayName, &ls))
	}
}

// wsErrorMessage turns a service error into a short, client-facing reason so
// the WS peer can tell apart "not a member" / "read-only" / "bad input" from a
// generic failure, mirroring the HTTP status mapping in response.RespondError.
func wsErrorMessage(err error) string {
	switch {
	case errors.Is(err, service.ErrForbidden):
		return "you are not a member of this chat"
	case errors.Is(err, service.ErrChatReadOnly):
		return "chat is read-only"
	case errors.Is(err, service.ErrInvalidInput):
		return "invalid message"
	default:
		return "failed to send message"
	}
}

func (c *Client) sendError(ctx context.Context, requestID, message string) {
	errPayload, err := json.Marshal(map[string]string{"message": message})
	if err != nil {
		c.log.Error("failed to marshal error message", slog.Any("err", err))
		return
	}
	errEvent := WSEvent{
		Type:      EventError,
		RequestID: requestID,
		Payload:   errPayload,
	}
	data, _ := json.Marshal(errEvent)

	select {
	case <-ctx.Done():
		return
	case c.send <- data:
	}
}

func (c *Client) writePump(ctx context.Context, cancel context.CancelFunc) {
	ticker := time.NewTicker(pingPeriod)
	// writePump owns the socket: closing it here unblocks readPump's
	// ReadMessage when teardown was initiated by the hub (cancel), not by a
	// client-side read error. conn.Close is idempotent, so readPump closing it
	// too is fine.
	defer func() {
		ticker.Stop()
		cancel()
		_ = c.conn.Close()
	}()

	for {
		select {
		case <-ctx.Done():
			// Teardown: try to send a close frame, then exit. c.send is never
			// closed (multiple senders), so this ctx signal is the only stop.
			// Best-effort: соединение закрывается в любом случае.
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
			return
		case message := <-c.send:
			// У gorilla SetWriteDeadline лишь запоминает значение и всегда
			// возвращает nil; реальная ошибка придёт из WriteMessage.
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			// Refresh presence TTL on the same tick as the WS ping so a live
			// connection never decays to offline. Best-effort, off the write path.
			if c.presence != nil {
				safego.Go(c.log, func() {
					hbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if err := c.presence.Heartbeat(hbCtx, c.userID); err != nil {
						c.log.Error("presence: heartbeat", slog.Any("err", err))
					}
				})
			}

			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
