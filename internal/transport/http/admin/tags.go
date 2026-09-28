package admin

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
)

const tagsPath = "/admin/tags"

// TagsPageData — данные экрана справочника тегов.
type TagsPageData struct {
	Tags []dto.AdminTagRow
}

// TagsList рисует справочник тегов.
func (h *Handler) TagsList(w http.ResponseWriter, r *http.Request) {
	admin, _ := middleware.GetAdminFromContext(r.Context())
	q := r.URL.Query()

	rows, err := h.tags.List(r.Context())
	if err != nil {
		h.log.Error("listing tags", slog.Any("error", err))
		h.render.Render(w, http.StatusInternalServerError, "tags", PageData{
			Title: "Теги", Admin: admin, Error: "Не удалось загрузить теги", Data: TagsPageData{},
		})
		return
	}

	h.render.Render(w, http.StatusOK, "tags", PageData{
		Title: "Теги",
		Admin: admin,
		Flash: flashTexts[notice(q.Get("flash"))],
		Error: errorTexts[notice(q.Get("err"))],
		Data:  TagsPageData{Tags: rows},
	})
}

// TagCreate добавляет тег.
func (h *Handler) TagCreate(w http.ResponseWriter, r *http.Request) {
	admin, ok := middleware.GetAdminFromContext(r.Context())
	if !ok || admin == nil {
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
		return
	}
	err := h.tags.Create(r.Context(), admin.ID, r.PostFormValue("name"), h.clientIP(r))
	h.finishTagAction(w, r, err, noticeTagCreated)
}

// TagRename переименовывает тег.
func (h *Handler) TagRename(w http.ResponseWriter, r *http.Request) {
	h.tagAction(w, r, noticeTagRenamed, func(actorID, tagID int64, ip string) error {
		return h.tags.Rename(r.Context(), actorID, tagID, r.PostFormValue("name"), ip)
	})
}

// TagHide скрывает тег из каталога.
func (h *Handler) TagHide(w http.ResponseWriter, r *http.Request) {
	h.tagAction(w, r, noticeTagHidden, func(actorID, tagID int64, ip string) error {
		return h.tags.SetHidden(r.Context(), actorID, tagID, true, ip)
	})
}

// TagUnhide возвращает тег в каталог.
func (h *Handler) TagUnhide(w http.ResponseWriter, r *http.Request) {
	h.tagAction(w, r, noticeTagUnhidden, func(actorID, tagID int64, ip string) error {
		return h.tags.SetHidden(r.Context(), actorID, tagID, false, ip)
	})
}

// tagAction — общая часть действий над существующим тегом: актор, id из
// пути, вызов, POST-redirect-GET на справочник.
func (h *Handler) tagAction(w http.ResponseWriter, r *http.Request, done notice, act func(actorID, tagID int64, ip string) error) {
	admin, ok := middleware.GetAdminFromContext(r.Context())
	if !ok || admin == nil {
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
		return
	}
	tagID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		redirectWithNotice(w, r, tagsPath, "", noticeTagNotFound)
		return
	}
	h.finishTagAction(w, r, act(admin.ID, tagID, h.clientIP(r)), done)
}

// finishTagAction переводит результат в уведомление.
func (h *Handler) finishTagAction(w http.ResponseWriter, r *http.Request, err error, done notice) {
	switch {
	case err == nil:
		redirectWithNotice(w, r, tagsPath, done, "")
	case errors.Is(err, service.ErrInvalidInput):
		redirectWithNotice(w, r, tagsPath, "", noticeTagNameInvalid)
	case errors.Is(err, service.ErrAlreadyExists):
		redirectWithNotice(w, r, tagsPath, "", noticeTagNameTaken)
	case errors.Is(err, service.ErrNotFound):
		redirectWithNotice(w, r, tagsPath, "", noticeTagNotFound)
	default:
		h.log.Error("changing tag", slog.Any("error", err))
		redirectWithNotice(w, r, tagsPath, "", noticeTagFailed)
	}
}
