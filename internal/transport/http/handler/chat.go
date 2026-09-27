package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/safego"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/response"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/websocket"
)

type ChatHandler struct {
	service  *service.ChatService
	presence *service.PresenceService
	hub      *websocket.Hub
	log      *slog.Logger
}

func NewChatHandler(service *service.ChatService, presence *service.PresenceService, hub *websocket.Hub, log *slog.Logger) *ChatHandler {
	return &ChatHandler{service: service, presence: presence, hub: hub, log: log}
}

// GetUserChats
// @Summary     Список чатов пользователя
// @Description Возвращает список всех чатов (групповых и личных), в которых состоит текущий авторизованный пользователь. Отсортированы по активности (чаты с новыми сообщениями — сверху). Включает текст последнего сообщения и счетчик непрочитанных.
// @Tags        Chats
// @Security    BearerAuth
// @Produce     json
// @Success     200     {array}   dto.ChatResponse
// @Failure     401     {object}  response.ErrorResponse "Не авторизован"
// @Failure     500     {object}  response.ErrorResponse "Внутренняя ошибка сервера"
// @Router      /v1/chats [get]
func (h *ChatHandler) GetUserChats(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r, h.log)
	if !ok {
		return
	}

	chats, err := h.service.GetUserChats(r.Context(), userID)
	if err != nil {
		response.RespondError(w, err, h.log)
		return
	}

	response.JSON(w, http.StatusOK, chats)
}

// GetMessages
// @Summary     Получение сообщений чата
// @Description Возвращает историю сообщений для конкретного чата. Поддерживает cursor-based пагинацию. Пользователь должен быть участником чата.
// @Tags        Chats
// @Security    BearerAuth
// @Produce     json
// @Param       id      path      int     true  "ID чата"
// @Param       cursor  query     int     false "ID последнего полученного сообщения (для пагинации, по умолчанию 0)"
// @Param       limit   query     int     false "Лимит сообщений (по умолчанию 50, максимум 100)"
// @Success     200     {array}   dto.MessageResponse
// @Failure     400     {object}  response.ErrorResponse "Неверный ID чата или параметры"
// @Failure     401     {object}  response.ErrorResponse "Не авторизован"
// @Failure     500     {object}  response.ErrorResponse "Внутренняя ошибка сервера"
// @Router      /v1/chats/{id}/messages [get]
func (h *ChatHandler) GetMessages(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r, h.log)
	if !ok {
		return
	}

	chatID, ok := pathInt64(w, r, "id", h.log)
	if !ok {
		return
	}

	query := r.URL.Query()

	cursor, _ := strconv.ParseInt(query.Get("cursor"), 10, 64)
	limit, _ := strconv.Atoi(query.Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	messages, err := h.service.GetMessages(r.Context(), userID, chatID, cursor, limit)
	if err != nil {
		response.RespondError(w, err, h.log)
		return
	}

	response.JSON(w, http.StatusOK, messages)
}

// GetChatPresence
// @Summary     Присутствие участников чата
// @Description Возвращает онлайн-статус и время последнего визита для всех участников чата. Вызывающий должен быть участником.
// @Tags        Chats
// @Security    BearerAuth
// @Produce     json
// @Param       id      path      int     true  "ID чата"
// @Success     200     {array}   dto.PresenceResponse
// @Failure     400     {object}  response.ErrorResponse "Неверный ID чата"
// @Failure     401     {object}  response.ErrorResponse "Не авторизован"
// @Failure     403     {object}  response.ErrorResponse "Нет доступа к чату"
// @Failure     500     {object}  response.ErrorResponse "Внутренняя ошибка сервера"
// @Router      /v1/chats/{id}/presence [get]
func (h *ChatHandler) GetChatPresence(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r, h.log)
	if !ok {
		return
	}

	chatID, ok := pathInt64(w, r, "id", h.log)
	if !ok {
		return
	}

	statuses, err := h.presence.StatusForChat(r.Context(), chatID, userID)
	if err != nil {
		response.RespondError(w, err, h.log)
		return
	}

	// Map service statuses to the DTO here: dto must not import service (that
	// would cycle, since service imports dto).
	out := make([]dto.PresenceResponse, 0, len(statuses))
	for _, st := range statuses {
		pr := dto.PresenceResponse{UserID: st.UserID, Online: st.Online, DisplayName: st.DisplayName}
		if st.LastSeen != nil {
			v := st.LastSeen.Unix()
			pr.LastSeen = &v
		}
		out = append(out, pr)
	}

	response.JSON(w, http.StatusOK, out)
}

type SendMessageRequest struct {
	Content          string  `json:"content" example:"Привет, как дела?"`
	ReplyToMessageID *int64  `json:"reply_to_message_id,omitempty" example:"42"`
	FileID           *string `json:"file_id,omitempty" example:"6f5e4d3c-2b1a-..."`
	// RequestID — клиентский idempotency key (UUID). Повторный POST с тем же
	// request_id не создаёт дубль, а возвращает уже созданное сообщение (200).
	RequestID *string `json:"request_id,omitempty" example:"a1b2c3d4-..."`
}

type EditMessageRequest struct {
	Content string `json:"content" example:"Исправленный текст"`
}

// SendMessage
// @Summary     Отправка сообщения
// @Description Сохраняет новое сообщение в базу и моментально рассылает его онлайн-участникам чата через WebSockets.
// @Tags        Chats
// @Security    BearerAuth
// @Accept      json
// @Produce     json
// @Param       id      path      int                 true  "ID чата"
// @Param       request body      SendMessageRequest  true  "Текст сообщения"
// @Success     201     {object}  dto.MessageResponse
// @Failure     400     {object}  response.ErrorResponse "Неверный формат запроса"
// @Failure     401     {object}  response.ErrorResponse "Не авторизован"
// @Failure     500     {object}  response.ErrorResponse "Внутренняя ошибка сервера"
// @Router      /v1/chats/{id}/messages [post]
func (h *ChatHandler) SendMessage(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r, h.log)
	if !ok {
		return
	}

	chatID, ok := pathInt64(w, r, "id", h.log)
	if !ok {
		return
	}

	var req SendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.RespondError(w, service.ErrInvalidInput, h.log)
		return
	}

	msg, targetIDs, created, err := h.service.SendMessage(r.Context(), chatID, userID, req.Content, req.ReplyToMessageID, req.FileID, req.RequestID)
	if err != nil {
		response.RespondError(w, err, h.log)
		return
	}

	// Идемпотентный повтор (тот же request_id): сообщение уже создано — не
	// рассылаем его снова и отвечаем 200 с существующим вместо 201.
	status := http.StatusCreated
	if created {
		// Рассылка асинхронна, чтобы не держать ответ на публикации в Redis, —
		// но через safego: паника в голой горутине не ловится Recoverer'ом
		// роутера и роняет весь инстанс по действию одного пользователя.
		safego.Go(h.log, func() { h.broadcastMessageEvent(websocket.EventNewMessage, msg, targetIDs) })
	} else {
		status = http.StatusOK
	}

	response.JSON(w, status, msg)
}

// EditMessage
// @Summary     Редактирование сообщения
// @Description Меняет текст своего сообщения. Редактировать может только автор. Изменение рассылается участникам чата через WebSocket.
// @Tags        Chats
// @Security    BearerAuth
// @Accept      json
// @Produce     json
// @Param       id        path      int                 true  "ID чата"
// @Param       messageId path      int                 true  "ID сообщения"
// @Param       request   body      EditMessageRequest  true  "Новый текст"
// @Success     200       {object}  dto.MessageResponse
// @Failure     400       {object}  response.ErrorResponse "Неверный запрос"
// @Failure     401       {object}  response.ErrorResponse "Не авторизован"
// @Failure     403       {object}  response.ErrorResponse "Не автор сообщения"
// @Failure     404       {object}  response.ErrorResponse "Сообщение не найдено"
// @Router      /v1/chats/{id}/messages/{messageId} [patch]
func (h *ChatHandler) EditMessage(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r, h.log)
	if !ok {
		return
	}

	chatID, ok := pathInt64(w, r, "id", h.log)
	if !ok {
		return
	}

	messageID, ok := pathInt64(w, r, "messageId", h.log)
	if !ok {
		return
	}

	var req EditMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.RespondError(w, service.ErrInvalidInput, h.log)
		return
	}

	msg, targetIDs, err := h.service.EditMessage(r.Context(), chatID, messageID, userID, req.Content)
	if err != nil {
		response.RespondError(w, err, h.log)
		return
	}

	safego.Go(h.log, func() { h.broadcastMessageEvent(websocket.EventMessageEdited, msg, targetIDs) })

	response.JSON(w, http.StatusOK, msg)
}

// DeleteMessage
// @Summary     Удаление сообщения
// @Description Удаляет своё сообщение (мягкое удаление). Удалять может только автор. Удаление рассылается участникам чата через WebSocket.
// @Tags        Chats
// @Security    BearerAuth
// @Produce     json
// @Param       id        path      int  true  "ID чата"
// @Param       messageId path      int  true  "ID сообщения"
// @Success     204       "Удалено"
// @Failure     401       {object}  response.ErrorResponse "Не авторизован"
// @Failure     403       {object}  response.ErrorResponse "Не автор сообщения"
// @Failure     404       {object}  response.ErrorResponse "Сообщение не найдено"
// @Router      /v1/chats/{id}/messages/{messageId} [delete]
func (h *ChatHandler) DeleteMessage(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r, h.log)
	if !ok {
		return
	}

	chatID, ok := pathInt64(w, r, "id", h.log)
	if !ok {
		return
	}

	messageID, ok := pathInt64(w, r, "messageId", h.log)
	if !ok {
		return
	}

	_, targetIDs, err := h.service.DeleteMessage(r.Context(), chatID, messageID, userID)
	if err != nil {
		response.RespondError(w, err, h.log)
		return
	}

	payload, _ := json.Marshal(websocket.WSMessageDeletedPayload{ChatID: chatID, MessageID: messageID})
	safego.Go(h.log, func() { h.broadcastRawEvent(websocket.EventMessageDeleted, payload, targetIDs) })

	w.WriteHeader(http.StatusNoContent)
}

// broadcastMessageEvent wraps a message DTO in a WS envelope of the given type
// and pushes it to the chat's participants.
func (h *ChatHandler) broadcastMessageEvent(eventType string, msg *dto.MessageResponse, targetIDs []int64) {
	payload, _ := json.Marshal(msg)
	h.broadcastRawEvent(eventType, payload, targetIDs)
}

// broadcastRawEvent wraps an already-encoded payload in a WS envelope and pushes
// it to the chat's participants.
func (h *ChatHandler) broadcastRawEvent(eventType string, payload json.RawMessage, targetIDs []int64) {
	event := websocket.WSEvent{Type: eventType, Payload: payload}
	finalPayload, _ := json.Marshal(event)
	h.hub.BroadcastToUsers(targetIDs, finalPayload)
}
