package admin

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
)

// AdminUserSvc — то, что транспорт требует от сервиса пользователей.
type AdminUserSvc interface {
	ListUsers(ctx context.Context, f service.AdminUserFilter) (dto.Page[dto.AdminUserRow], error)
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
