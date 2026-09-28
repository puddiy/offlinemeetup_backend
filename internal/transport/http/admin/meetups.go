package admin

import (
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
)

const meetupsPath = "/admin/meetups"

// meetupStatusFilters — допустимые значения ?status=. Остальное молча
// превращается в «все», как у списка жалоб.
var meetupStatusFilters = map[string]bool{"active": true, "past": true, "cancelled": true}

// adminLocation — пояс, в котором админка показывает и принимает время
// митапов. Task 9 подставляет cfg.AdminTimezone; до тех пор — UTC.
func (h *Handler) adminLocation() *time.Location {
	return time.UTC
}

// isAdminRole — роль admin у текущего администратора. Только для того,
// чтобы не рисовать кнопки, которые вернут 403; защита — в routes.go.
func isAdminRole(a *domain.AdminUser) bool {
	return a != nil && a.Role == domain.AdminRoleAdmin
}

// MeetupsPageData — данные экрана списка.
type MeetupsPageData struct {
	Page         dto.Page[dto.AdminMeetupRow]
	Search       string
	Status       string
	OnlyOfficial bool
	CanPublish   bool
	Loc          *time.Location
}

// Local форматирует время в поясе админки.
func (d MeetupsPageData) Local(t time.Time) string {
	return t.In(d.Loc).Format("2006-01-02 15:04")
}

// LinkWithOffset — ПОЛНАЯ ссылка на страницу списка (см. UsersPageData.LinkWithOffset).
func (d MeetupsPageData) LinkWithOffset(offset int) template.URL {
	v := url.Values{}
	if d.Search != "" {
		v.Set("q", d.Search)
	}
	if d.Status != "" {
		v.Set("status", d.Status)
	}
	if d.OnlyOfficial {
		v.Set("official", "1")
	}
	v.Set("limit", strconv.Itoa(d.Page.Limit))
	v.Set("offset", strconv.Itoa(offset))
	return template.URL(meetupsPath + "?" + v.Encode())
}

// MeetupsList рисует список митапов.
func (h *Handler) MeetupsList(w http.ResponseWriter, r *http.Request) {
	admin, _ := middleware.GetAdminFromContext(r.Context())
	q := r.URL.Query()

	data := MeetupsPageData{
		Search:       q.Get("q"),
		OnlyOfficial: q.Get("official") == "1",
		CanPublish:   isAdminRole(admin),
		Loc:          h.adminLocation(),
	}
	if s := q.Get("status"); meetupStatusFilters[s] {
		data.Status = s
	}

	page, err := h.meetups.List(r.Context(), service.AdminMeetupFilter{
		Search:       data.Search,
		Status:       data.Status,
		OnlyOfficial: data.OnlyOfficial,
		Limit:        atoiDefault(q.Get("limit"), 0),
		Offset:       atoiDefault(q.Get("offset"), 0),
	})
	if err != nil {
		h.log.Error("listing meetups", slog.Any("error", err))
		h.render.Render(w, http.StatusInternalServerError, "meetups", PageData{
			Title: "Митапы", Admin: admin, Error: "Не удалось загрузить митапы", Data: data,
		})
		return
	}
	data.Page = page

	h.render.Render(w, http.StatusOK, "meetups", PageData{
		Title: "Митапы",
		Admin: admin,
		Flash: flashTexts[notice(q.Get("flash"))],
		Error: errorTexts[notice(q.Get("err"))],
		Data:  data,
	})
}

// MeetupDetailData — данные карточки.
type MeetupDetailData struct {
	Meetup  *dto.AdminMeetupDetail
	CanEdit bool
	Loc     *time.Location
}

// Local форматирует время в поясе админки.
func (d MeetupDetailData) Local(t time.Time) string {
	return t.In(d.Loc).Format("2006-01-02 15:04")
}

// MeetupDetail рисует карточку митапа.
func (h *Handler) MeetupDetail(w http.ResponseWriter, r *http.Request) {
	admin, _ := middleware.GetAdminFromContext(r.Context())

	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		redirectWithNotice(w, r, meetupsPath, "", noticeMeetupNotFound)
		return
	}

	m, err := h.meetups.Get(r.Context(), id)
	if err != nil {
		status, text := http.StatusInternalServerError, "Не удалось загрузить митап"
		if errors.Is(err, service.ErrNotFound) {
			status, text = http.StatusNotFound, "Митап не найден"
		} else {
			h.log.Error("loading meetup", slog.Int64("meetup_id", id), slog.Any("error", err))
		}
		h.render.Render(w, status, "meetups", PageData{
			Title: "Митапы", Admin: admin, Error: text, Data: MeetupsPageData{Loc: h.adminLocation()},
		})
		return
	}

	q := r.URL.Query()
	h.render.Render(w, http.StatusOK, "meetup_detail", PageData{
		Title: "Митап",
		Admin: admin,
		Flash: flashTexts[notice(q.Get("flash"))],
		Error: errorTexts[notice(q.Get("err"))],
		Data: MeetupDetailData{
			Meetup:  m,
			CanEdit: m.IsOfficial && m.Status == "active" && isAdminRole(admin),
			Loc:     h.adminLocation(),
		},
	})
}

// MeetupCancel отменяет митап со страницы митапа (модерация, обе роли).
func (h *Handler) MeetupCancel(w http.ResponseWriter, r *http.Request) {
	admin, ok := middleware.GetAdminFromContext(r.Context())
	if !ok || admin == nil {
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		redirectWithNotice(w, r, meetupsPath, "", noticeMeetupNotFound)
		return
	}

	switch err := h.moderation.CancelMeetupByAdmin(r.Context(), admin.ID, id, h.clientIP(r)); {
	case err == nil:
		h.redirectToMeetup(w, r, id, noticeMeetupCancelled, "")
	case errors.Is(err, service.ErrTargetGone):
		h.redirectToMeetup(w, r, id, "", noticeMeetupNotActive)
	default:
		h.log.Error("cancelling meetup", slog.Int64("meetup_id", id), slog.Any("error", err))
		h.redirectToMeetup(w, r, id, "", noticeModerationFailed)
	}
}

// redirectToMeetup — POST-redirect-GET на карточку митапа.
func (h *Handler) redirectToMeetup(w http.ResponseWriter, r *http.Request, id int64, flash, errKey notice) {
	redirectWithNotice(w, r, meetupsPath+"/"+strconv.FormatInt(id, 10), flash, errKey)
}
