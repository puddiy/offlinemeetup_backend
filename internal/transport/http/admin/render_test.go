package admin

import (
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/stretchr/testify/require"
)

// Шаблоны парсятся на старте приложения, поэтому синтаксическая ошибка в
// .gohtml обязана ронять сборку тестов, а не прод в рантайме.
func TestNewRendererParsesTemplates(t *testing.T) {
	r, err := NewRenderer(slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.NotNil(t, r)
}

func TestRenderLoginPage(t *testing.T) {
	r, err := NewRenderer(slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	r.Render(rec, 200, "login", PageData{Title: "Вход"})

	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	body := rec.Body.String()
	require.Contains(t, body, `name="email"`)
	require.Contains(t, body, `name="password"`)
	require.Contains(t, body, `method="post"`)
}

func TestRenderDashboardShowsAdmin(t *testing.T) {
	r, err := NewRenderer(slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	r.Render(rec, 200, "dashboard", PageData{
		Title: "Дашборд",
		Admin: &domain.AdminUser{ID: 1, Email: "a@x.io", Role: domain.AdminRoleAdmin},
	})

	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), "a@x.io")
}

// Пользовательский ввод обязан экранироваться — это и есть причина брать
// html/template, а не text/template.
func TestRenderEscapesUserInput(t *testing.T) {
	r, err := NewRenderer(slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	r.Render(rec, 200, "login", PageData{Error: `<script>alert(1)</script>`})

	body := rec.Body.String()
	require.NotContains(t, body, "<script>alert(1)</script>")
	require.Contains(t, body, "&lt;script&gt;")
}

func TestRenderUnknownTemplateIs500(t *testing.T) {
	r, err := NewRenderer(slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	r.Render(rec, 200, "no-such-template", PageData{})

	require.Equal(t, 500, rec.Code)
	require.False(t, strings.Contains(rec.Body.String(), "no-such-template"),
		"имя шаблона — внутренняя деталь, наружу её не отдаём")
}

func TestRenderPartialHasNoLayout(t *testing.T) {
	r, err := NewRenderer(slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	r.RenderPartial(rec, 200, "users_table", UsersPageData{
		Page: dto.NewPage([]dto.AdminUserRow{{ID: 1, Email: "a@x.io", Username: "alice", Status: "active"}}, 1, 20, 0),
	})

	require.Equal(t, 200, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "a@x.io")
	require.NotContains(t, body, "<!DOCTYPE html>", "фрагмент не должен тащить layout")
}

func TestRenderUsersPageIncludesLayoutAndTable(t *testing.T) {
	r, err := NewRenderer(slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	r.Render(rec, 200, "users", PageData{
		Title: "Пользователи",
		Admin: &domain.AdminUser{ID: 1, Email: "root@x.io", Role: domain.AdminRoleAdmin},
		Data: UsersPageData{
			Page: dto.NewPage([]dto.AdminUserRow{{ID: 2, Email: "b@x.io", Username: "bob", Status: "banned"}}, 1, 20, 0),
		},
	})

	require.Equal(t, 200, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "<!DOCTYPE html>")
	require.Contains(t, body, "b@x.io")
}
