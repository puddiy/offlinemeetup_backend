package admin

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
)

// AdminUserSvc — то, что транспорт требует от сервиса пользователей.
type AdminUserSvc interface {
	ListUsers(ctx context.Context, f service.AdminUserFilter) (dto.Page[dto.AdminUserRow], error)
	SetStatus(ctx context.Context, actorID, userID int64, status domain.UserStatus, ip string) error
	LogoutEverywhere(ctx context.Context, actorID, userID int64, ip string) error
}

// UsersPageData — данные экрана списка. Кладётся в PageData.Data.
type UsersPageData struct {
	Page        dto.Page[dto.AdminUserRow]
	Search      string
	Status      string
	OnlyDeleted bool
}

// QueryWithOffset собирает query-строку пагинации, сохраняя текущие фильтры.
// Метод на данных, а не конкатенация в шаблоне: url.Values экранирует
// пользовательский ввод, а ручная склейка в {{...}} — нет.
func (d UsersPageData) QueryWithOffset(offset int) string {
	v := url.Values{}
	if d.Search != "" {
		v.Set("q", d.Search)
	}
	if d.Status != "" {
		v.Set("status", d.Status)
	}
	if d.OnlyDeleted {
		v.Set("deleted", "1")
	}
	v.Set("limit", strconv.Itoa(d.Page.Limit))
	v.Set("offset", strconv.Itoa(offset))
	return v.Encode()
}

// UsersList рисует список. На HTMX-запрос (заголовок HX-Request) отдаёт
// только фрагмент таблицы, чтобы поиск не перерисовывал всю страницу.
func (h *Handler) UsersList(w http.ResponseWriter, r *http.Request) {
	admin, _ := middleware.GetAdminFromContext(r.Context())

	q := r.URL.Query()
	data := UsersPageData{
		Search:      q.Get("q"),
		Status:      q.Get("status"),
		OnlyDeleted: q.Get("deleted") == "1",
	}

	page, err := h.users.ListUsers(r.Context(), service.AdminUserFilter{
		Search:      data.Search,
		Status:      data.Status,
		OnlyDeleted: data.OnlyDeleted,
		Limit:       atoiDefault(q.Get("limit"), 0),
		Offset:      atoiDefault(q.Get("offset"), 0),
	})
	if err != nil {
		h.log.Error("listing users", "error", err)
		h.render.Render(w, http.StatusInternalServerError, "users", PageData{
			Title: "Пользователи",
			Admin: admin,
			Error: "Не удалось загрузить список",
			Data:  data,
		})
		return
	}
	data.Page = page

	if r.Header.Get("HX-Request") == "true" {
		h.render.RenderPartial(w, http.StatusOK, "users_table", data)
		return
	}

	h.render.Render(w, http.StatusOK, "users", PageData{
		Title: "Пользователи",
		Admin: admin,
		Data:  data,
	})
}

// atoiDefault разбирает целое из query-строки, подставляя def на мусоре.
// Кривой ?limit=abc — это не повод на 400: поддержка просто увидит
// страницу с настройками по умолчанию.
func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// UserBan блокирует пользователя. Действует немедленно: AuthMiddleware
// перечитывает статус на каждом авторизованном запросе.
func (h *Handler) UserBan(w http.ResponseWriter, r *http.Request) {
	h.setUserStatus(w, r, domain.UserStatusBanned, "Пользователь заблокирован")
}

// UserUnban снимает блокировку.
func (h *Handler) UserUnban(w http.ResponseWriter, r *http.Request) {
	h.setUserStatus(w, r, domain.UserStatusActive, "Блокировка снята")
}

// setUserStatus — общая часть бана и разбана: достать актора и id, вызвать
// сервис, вернуться на карточку с сообщением (POST-redirect-GET, чтобы
// обновление страницы не повторяло действие).
func (h *Handler) setUserStatus(w http.ResponseWriter, r *http.Request, status domain.UserStatus, okMessage string) {
	admin, ok := middleware.GetAdminFromContext(r.Context())
	if !ok || admin == nil {
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
		return
	}

	userID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}

	if err := h.users.SetStatus(r.Context(), admin.ID, userID, status, h.clientIP(r)); err != nil {
		h.log.Error("changing user status",
			slog.Int64("user_id", userID), slog.String("status", string(status)), slog.Any("error", err))
		h.redirectToUser(w, r, userID, "", "Не удалось изменить статус")
		return
	}

	h.redirectToUser(w, r, userID, okMessage, "")
}

// redirectToUser возвращает на карточку пользователя, передавая результат
// действия query-параметрами. Флеш в query, а не в сессии: сессия админа
// лежит в Redis и общая для вкладок — сообщение из одной вкладки всплыло бы
// в другой.
func (h *Handler) redirectToUser(w http.ResponseWriter, r *http.Request, userID int64, flash, errMsg string) {
	v := url.Values{}
	if flash != "" {
		v.Set("flash", flash)
	}
	if errMsg != "" {
		v.Set("err", errMsg)
	}
	target := "/admin/users/" + strconv.FormatInt(userID, 10)
	if q := v.Encode(); q != "" {
		target += "?" + q
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// UserLogoutAll отзывает все сессии пользователя.
func (h *Handler) UserLogoutAll(w http.ResponseWriter, r *http.Request) {
	admin, ok := middleware.GetAdminFromContext(r.Context())
	if !ok || admin == nil {
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
		return
	}

	userID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}

	if err := h.users.LogoutEverywhere(r.Context(), admin.ID, userID, h.clientIP(r)); err != nil {
		h.log.Error("revoking user sessions", slog.Int64("user_id", userID), slog.Any("error", err))
		h.redirectToUser(w, r, userID, "", "Не удалось отозвать сессии")
		return
	}

	h.redirectToUser(w, r, userID, "Сессии отозваны (access-токен живёт ещё до 15 минут)", "")
}
