package admin

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-chi/chi/v5"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/config"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newTestRoutes(t *testing.T) http.Handler {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	log := slog.New(slog.DiscardHandler)
	rend, err := NewRenderer(log)
	require.NoError(t, err)
	cfg := &config.Config{Env: "local"}
	h := NewHandler(&stubAuth{loginErr: service.ErrUnauthorized}, nil, &recordingAudit{}, rend, cfg, log)

	root := chi.NewRouter()
	root.Mount("/admin", Routes(h, rdb, log, cfg))
	return root
}

func serve(root http.Handler, req *http.Request) *httptest.ResponseRecorder {
	req.RemoteAddr = "7.7.7.7:5555"
	rec := httptest.NewRecorder()
	root.ServeHTTP(rec, req)
	return rec
}

func loginPost(origin string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "http://example.com/admin/login",
		strings.NewReader(url.Values{"email": {"a@x.io"}, "password": {"pw"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", origin)
	return req
}

func TestRoutesBasics(t *testing.T) {
	root := newTestRoutes(t)

	rec := serve(root, httptest.NewRequest(http.MethodGet, "/admin/", nil))
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/admin/login", rec.Header().Get("Location"))

	rec = serve(root, httptest.NewRequest(http.MethodGet, "/admin/login", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	rec = serve(root, httptest.NewRequest(http.MethodGet, "/admin/static/htmx.min.js", nil))
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestRoutesLoginGetNotThrottled(t *testing.T) {
	root := newTestRoutes(t)
	for i := 0; i < 15; i++ {
		rec := serve(root, httptest.NewRequest(http.MethodGet, "/admin/login", nil))
		require.Equal(t, http.StatusOK, rec.Code, "GET #%d", i+1)
	}
}

func TestRoutesLoginPostThrottled(t *testing.T) {
	root := newTestRoutes(t)
	for i := 0; i < 10; i++ {
		rec := serve(root, loginPost("http://example.com"))
		require.Equal(t, http.StatusUnauthorized, rec.Code, "POST #%d", i+1)
	}
	rec := serve(root, loginPost("http://example.com"))
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
}

func TestRoutesLoginPostCrossOriginForbidden(t *testing.T) {
	root := newTestRoutes(t)
	rec := serve(root, loginPost("http://evil.example"))
	require.Equal(t, http.StatusForbidden, rec.Code)
}

// newRoutesAs собирает поддерево /admin с уже залогиненным админом заданной
// роли: stubAuth.Authenticate отдаёт его на любую cookie.
func newRoutesAs(t *testing.T, role domain.AdminRole, users AdminUserSvc) http.Handler {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	log := slog.New(slog.DiscardHandler)
	rend, err := NewRenderer(log)
	require.NoError(t, err)
	cfg := &config.Config{Env: "local"}

	auth := &stubAuth{admin: &domain.AdminUser{ID: 7, Email: "root@x.io", Role: role, IsActive: true}}
	h := NewHandler(auth, users, &recordingAudit{}, rend, cfg, log)

	root := chi.NewRouter()
	root.Mount("/admin", Routes(h, rdb, log, cfg))
	return root
}

func deletePost(t *testing.T) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/admin/users/42/delete", nil)
	req.Header.Set("Origin", "http://example.com")
	req.AddCookie(&http.Cookie{Name: middleware.AdminSessionCookieName, Value: "live-session"})
	return req
}

// Находка ревью: удаление аккаунта не было закрыто ролью, и модератор мог
// безвозвратно анонимизировать любого пользователя. Гейт живёт в маршруте —
// спрятанной кнопки в шаблоне мало, POST по URL обходит её за один запрос.
func TestRoutesDeleteForbiddenForModerator(t *testing.T) {
	users := &stubUserSvc{}
	root := newRoutesAs(t, domain.AdminRoleModerator, users)

	rec := serve(root, deletePost(t))

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Zero(t, users.gotUserID, "сервис не должен быть вызван вообще")
}

func TestRoutesDeleteAllowedForAdmin(t *testing.T) {
	users := &stubUserSvc{}
	root := newRoutesAs(t, domain.AdminRoleAdmin, users)

	rec := serve(root, deletePost(t))

	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, int64(42), users.gotUserID)
}

// Бан и отзыв сессий обратимы и модератору положены — гейт на них вешать
// не надо, и этот тест ловит попытку «на всякий случай» закрыть и их.
func TestRoutesBanStaysAllowedForModerator(t *testing.T) {
	users := &stubUserSvc{}
	root := newRoutesAs(t, domain.AdminRoleModerator, users)

	req := httptest.NewRequest(http.MethodPost, "http://example.com/admin/users/42/ban", nil)
	req.Header.Set("Origin", "http://example.com")
	req.AddCookie(&http.Cookie{Name: middleware.AdminSessionCookieName, Value: "live-session"})

	rec := serve(root, req)

	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, int64(42), users.gotUserID)
}
