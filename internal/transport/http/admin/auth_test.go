package admin

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/config"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

type stubAuth struct {
	token     string
	admin     *domain.AdminUser
	loginErr  error
	logoutErr error
}

func (s *stubAuth) Login(_ context.Context, _, _ string) (string, *domain.AdminUser, error) {
	if s.loginErr != nil {
		return "", nil, s.loginErr
	}
	return s.token, s.admin, nil
}

func (s *stubAuth) Authenticate(_ context.Context, _ string) (*domain.AdminUser, error) {
	return s.admin, nil
}

func (s *stubAuth) Logout(_ context.Context, _ string) error { return s.logoutErr }

type recordingAudit struct{ events []service.AuditEvent }

func (a *recordingAudit) Record(_ context.Context, _ bun.IDB, ev service.AuditEvent) error {
	a.events = append(a.events, ev)
	return nil
}

func newTestHandler(t *testing.T, auth *stubAuth, audit *recordingAudit) *Handler {
	t.Helper()
	r, err := NewRenderer(slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	cfg := &config.Config{Env: "local"}
	return NewHandler(auth, audit, r, cfg, slog.New(slog.DiscardHandler))
}

func postForm(target string, values url.Values) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func TestLoginFormRenders(t *testing.T) {
	h := newTestHandler(t, &stubAuth{}, &recordingAudit{})

	rec := httptest.NewRecorder()
	h.LoginForm(rec, httptest.NewRequest(http.MethodGet, "/admin/login", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `name="password"`)
}

func TestLoginSubmitSuccessSetsCookieAndRedirects(t *testing.T) {
	admin := &domain.AdminUser{ID: 3, Email: "a@x.io", Role: domain.AdminRoleAdmin}
	audit := &recordingAudit{}
	h := newTestHandler(t, &stubAuth{token: "tok-123", admin: admin}, audit)

	rec := httptest.NewRecorder()
	h.LoginSubmit(rec, postForm("/admin/login", url.Values{
		"email":    {"a@x.io"},
		"password": {"pw"},
	}))

	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/admin/", rec.Header().Get("Location"))

	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)
	c := cookies[0]
	require.Equal(t, middleware.AdminSessionCookieName, c.Name)
	require.Equal(t, "tok-123", c.Value)
	require.True(t, c.HttpOnly, "cookie сессии обязана быть HttpOnly")
	require.Equal(t, http.SameSiteStrictMode, c.SameSite)
	require.Equal(t, "/admin", c.Path)

	require.Len(t, audit.events, 1)
	require.Equal(t, service.AuditActionLogin, audit.events[0].Action)
	require.Equal(t, int64(3), audit.events[0].AdminID)
}

func TestLoginSubmitFailureRendersFormWithoutCookie(t *testing.T) {
	audit := &recordingAudit{}
	h := newTestHandler(t, &stubAuth{loginErr: service.ErrUnauthorized}, audit)

	rec := httptest.NewRecorder()
	h.LoginSubmit(rec, postForm("/admin/login", url.Values{
		"email":    {"a@x.io"},
		"password": {"nope"},
	}))

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Empty(t, rec.Result().Cookies(), "неудачный вход не ставит cookie")
	require.Contains(t, rec.Body.String(), "Неверный email или пароль")
	require.Empty(t, audit.events, "неудачный вход не пишется как успешный")
}

// Ответ на неверный пароль и на несуществующий email обязан быть
// побайтово одинаковым — иначе форма становится оракулом.
func TestLoginSubmitErrorMessageIsUniform(t *testing.T) {
	h := newTestHandler(t, &stubAuth{loginErr: service.ErrUnauthorized}, &recordingAudit{})

	rec1 := httptest.NewRecorder()
	h.LoginSubmit(rec1, postForm("/admin/login", url.Values{"email": {"real@x.io"}, "password": {"bad"}}))

	rec2 := httptest.NewRecorder()
	h.LoginSubmit(rec2, postForm("/admin/login", url.Values{"email": {"ghost@x.io"}, "password": {"bad"}}))

	require.Equal(t, rec1.Body.String(), rec2.Body.String())
	require.Equal(t, rec1.Code, rec2.Code)
}

func TestLogoutClearsCookieAndAudits(t *testing.T) {
	admin := &domain.AdminUser{ID: 3, Email: "a@x.io", Role: domain.AdminRoleAdmin}
	audit := &recordingAudit{}
	h := newTestHandler(t, &stubAuth{admin: admin}, audit)

	req := postForm("/admin/logout", url.Values{})
	req.AddCookie(&http.Cookie{Name: middleware.AdminSessionCookieName, Value: "tok-123"})
	req = req.WithContext(context.WithValue(req.Context(), middleware.AdminKey, admin))

	rec := httptest.NewRecorder()
	h.Logout(rec, req)

	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/admin/login", rec.Header().Get("Location"))

	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)
	require.Equal(t, "", cookies[0].Value)
	require.Equal(t, -1, cookies[0].MaxAge)

	require.Len(t, audit.events, 1)
	require.Equal(t, service.AuditActionLogout, audit.events[0].Action)
}

func TestDashboardRendersForAdmin(t *testing.T) {
	admin := &domain.AdminUser{ID: 3, Email: "a@x.io", Role: domain.AdminRoleAdmin}
	h := newTestHandler(t, &stubAuth{admin: admin}, &recordingAudit{})

	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req = req.WithContext(context.WithValue(req.Context(), middleware.AdminKey, admin))

	rec := httptest.NewRecorder()
	h.Dashboard(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "a@x.io")
}
