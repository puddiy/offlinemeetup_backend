package admin

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/websocket"
)

// ModerationSvc — то, что транспорт требует от сервиса модерации.
type ModerationSvc interface {
	ListReports(ctx context.Context, f service.ReportFilter) (dto.Page[dto.AdminReportRow], error)
	GetReport(ctx context.Context, id int64) (*dto.AdminReportDetail, error)
	Dismiss(ctx context.Context, actorID, reportID int64, ip string) error
	CancelMeetup(ctx context.Context, actorID, reportID int64, ip string) error
	DeleteMessage(ctx context.Context, actorID, reportID int64, ip string) (*service.MessageRemoval, error)
	RemoveAvatar(ctx context.Context, actorID, reportID int64, ip string) error
	RemoveCover(ctx context.Context, actorID, reportID int64, ip string) error
}

// Broadcaster — рассылка WS-событий пользователям. Удовлетворяется *websocket.Hub.
type Broadcaster interface {
	BroadcastToUsers(targetIDs []int64, payload []byte)
}

// Допустимые значения фильтров в URL. Всё остальное молча заменяется
// значением по умолчанию: кривой ?status= не повод для 400.
var (
	reportStatusFilters = map[string]bool{"open": true, "resolved": true, "dismissed": true, "all": true}
	reportTypeFilters   = map[string]bool{"meetup": true, "message": true, "user": true}
)

// ReportsPageData — данные экрана очереди. Status — значение фильтра из URL
// ("all" — без фильтра), TargetType — "" или тип цели.
type ReportsPageData struct {
	Page       dto.Page[dto.AdminReportRow]
	Status     string
	TargetType string
}

// LinkWithOffset собирает ПОЛНУЮ ссылку на страницу очереди с текущими
// фильтрами. Почему целиком и типом template.URL — см. UsersPageData.LinkWithOffset:
// `href="/admin/reports?{{...}}"` html/template экранирует в одно имя без значения.
func (d ReportsPageData) LinkWithOffset(offset int) template.URL {
	v := url.Values{}
	v.Set("status", d.Status)
	if d.TargetType != "" {
		v.Set("type", d.TargetType)
	}
	v.Set("limit", strconv.Itoa(d.Page.Limit))
	v.Set("offset", strconv.Itoa(offset))
	return template.URL("/admin/reports?" + v.Encode())
}

// ReportsList рисует очередь жалоб. По умолчанию — открытые, от старых к новым.
func (h *Handler) ReportsList(w http.ResponseWriter, r *http.Request) {
	admin, _ := middleware.GetAdminFromContext(r.Context())
	q := r.URL.Query()

	data := ReportsPageData{Status: "open"}
	if s := q.Get("status"); reportStatusFilters[s] {
		data.Status = s
	}
	if t := q.Get("type"); reportTypeFilters[t] {
		data.TargetType = t
	}

	status := data.Status
	if status == "all" {
		status = ""
	}

	page, err := h.moderation.ListReports(r.Context(), service.ReportFilter{
		Status:     status,
		TargetType: data.TargetType,
		Limit:      atoiDefault(q.Get("limit"), 0),
		Offset:     atoiDefault(q.Get("offset"), 0),
	})
	if err != nil {
		h.log.Error("listing reports", slog.Any("error", err))
		h.render.Render(w, http.StatusInternalServerError, "reports", PageData{
			Title: "Жалобы",
			Admin: admin,
			Error: "Не удалось загрузить жалобы",
			Data:  data,
		})
		return
	}
	data.Page = page

	h.render.Render(w, http.StatusOK, "reports", PageData{
		Title: "Жалобы",
		Admin: admin,
		Data:  data,
	})
}

// ReportDetail рисует карточку жалобы.
func (h *Handler) ReportDetail(w http.ResponseWriter, r *http.Request) {
	admin, _ := middleware.GetAdminFromContext(r.Context())

	reportID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/admin/reports", http.StatusSeeOther)
		return
	}

	detail, err := h.moderation.GetReport(r.Context(), reportID)
	if err != nil {
		status, msg := http.StatusInternalServerError, "Не удалось загрузить жалобу"
		if errors.Is(err, service.ErrNotFound) {
			status, msg = http.StatusNotFound, "Жалоба не найдена"
		} else {
			h.log.Error("loading report", slog.Int64("report_id", reportID), slog.Any("error", err))
		}
		h.render.Render(w, status, "reports", PageData{
			Title: "Жалобы",
			Admin: admin,
			Error: msg,
			Data:  ReportsPageData{Status: "open"},
		})
		return
	}

	q := r.URL.Query()
	h.render.Render(w, http.StatusOK, "report_detail", PageData{
		Title: "Жалоба",
		Admin: admin,
		Flash: flashTexts[notice(q.Get("flash"))],
		Error: errorTexts[notice(q.Get("err"))],
		Data:  detail,
	})
}

// moderationAction — общая форма действий сервиса модерации.
type moderationAction func(ctx context.Context, actorID, reportID int64, ip string) error

func (h *Handler) ReportDismiss(w http.ResponseWriter, r *http.Request) {
	h.reportAction(w, r, noticeReportDismissed, h.moderation.Dismiss)
}

func (h *Handler) ReportCancelMeetup(w http.ResponseWriter, r *http.Request) {
	h.reportAction(w, r, noticeMeetupCancelled, h.moderation.CancelMeetup)
}

func (h *Handler) ReportRemoveAvatar(w http.ResponseWriter, r *http.Request) {
	h.reportAction(w, r, noticeAvatarRemoved, h.moderation.RemoveAvatar)
}

func (h *Handler) ReportRemoveCover(w http.ResponseWriter, r *http.Request) {
	h.reportAction(w, r, noticeCoverRemoved, h.moderation.RemoveCover)
}

// ReportDeleteMessage удаляет сообщение и рассылает messageDeleted участникам.
func (h *Handler) ReportDeleteMessage(w http.ResponseWriter, r *http.Request) {
	h.reportAction(w, r, noticeMessageDeleted, func(ctx context.Context, actorID, reportID int64, ip string) error {
		removal, err := h.moderation.DeleteMessage(ctx, actorID, reportID, ip)
		if err != nil {
			return err
		}
		h.broadcastMessageDeleted(removal)
		return nil
	})
}

// reportAction — общая часть действий: актор, id жалобы, вызов сервиса,
// POST-redirect-GET на карточку с ключом уведомления.
func (h *Handler) reportAction(w http.ResponseWriter, r *http.Request, done notice, act moderationAction) {
	admin, ok := middleware.GetAdminFromContext(r.Context())
	if !ok || admin == nil {
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
		return
	}

	reportID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/admin/reports", http.StatusSeeOther)
		return
	}

	target := "/admin/reports/" + strconv.FormatInt(reportID, 10)
	switch err := act(r.Context(), admin.ID, reportID, h.clientIP(r)); {
	case err == nil:
		redirectWithNotice(w, r, target, done, "")
	case errors.Is(err, service.ErrReportClosed):
		redirectWithNotice(w, r, target, "", noticeReportClosed)
	case errors.Is(err, service.ErrTargetGone):
		redirectWithNotice(w, r, target, "", noticeTargetGone)
	case errors.Is(err, service.ErrNotFound):
		http.Redirect(w, r, "/admin/reports", http.StatusSeeOther)
	case errors.Is(err, service.ErrInvalidInput):
		redirectWithNotice(w, r, target, "", noticeWrongAction)
	default:
		h.log.Error("moderation action failed",
			slog.Int64("report_id", reportID), slog.Int64("admin_id", admin.ID), slog.Any("error", err))
		redirectWithNotice(w, r, target, "", noticeModerationFailed)
	}
}

// broadcastMessageDeleted рассылает участникам чата то же событие, что и
// удаление автором (handler/chat.go), — иначе сообщение останется на их
// экранах до перезахода в чат.
//
// Синхронно, без горутины: Hub.BroadcastToUsers только публикует в Redis и
// сам логирует сбой. Голая горутина здесь нарушила бы правило safeGo.
func (h *Handler) broadcastMessageDeleted(m *service.MessageRemoval) {
	if h.ws == nil || m == nil || len(m.ParticipantIDs) == 0 {
		return
	}
	payload, err := json.Marshal(websocket.WSMessageDeletedPayload{ChatID: m.ChatID, MessageID: m.MessageID})
	if err != nil {
		h.log.Error("encoding messageDeleted payload", slog.Any("error", err))
		return
	}
	event, err := json.Marshal(websocket.WSEvent{Type: websocket.EventMessageDeleted, Payload: payload})
	if err != nil {
		h.log.Error("encoding messageDeleted event", slog.Any("error", err))
		return
	}
	h.ws.BroadcastToUsers(m.ParticipantIDs, event)
}
