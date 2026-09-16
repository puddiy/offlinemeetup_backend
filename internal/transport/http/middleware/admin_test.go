package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/stretchr/testify/require"
)

type stubAdminAuth struct {
	admin *domain.AdminUser
	err   error
}

func (s stubAdminAuth) Authenticate(_ context.Context, _ string) (*domain.AdminUser, error) {
	return s.admin, s.err
}

// okHandler отмечает, что запрос дошёл до цели.
func okHandler(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	})
}

func TestAdminSessionNoCookieRedirects(t *testing.T) {
	reached := false
	h := AdminSession(stubAdminAuth{}, "/admin/login")(okHandler(&reached))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/", nil))

	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/admin/login", rec.Header().Get("Location"))
	require.False(t, reached)
}

func TestAdminSessionInvalidTokenRedirects(t *testing.T) {
	reached := false
	h := AdminSession(stubAdminAuth{err: service.ErrUnauthorized}, "/admin/login")(okHandler(&reached))

	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.AddCookie(&http.Cookie{Name: AdminSessionCookieName, Value: "bad"})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.False(t, reached)
}

func TestAdminSessionValidTokenPasses(t *testing.T) {
	admin := &domain.AdminUser{ID: 5, Role: domain.AdminRoleModerator, IsActive: true}

	var seen *domain.AdminUser
	h := AdminSession(stubAdminAuth{admin: admin}, "/admin/login")(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen, _ = GetAdminFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.AddCookie(&http.Cookie{Name: AdminSessionCookieName, Value: "good"})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, seen)
	require.Equal(t, int64(5), seen.ID)
}

func TestRequireAdminRole(t *testing.T) {
	cases := []struct {
		name     string
		have     domain.AdminRole
		allowed  []domain.AdminRole
		wantCode int
	}{
		{"admin on admin-only", domain.AdminRoleAdmin, []domain.AdminRole{domain.AdminRoleAdmin}, http.StatusOK},
		{"moderator on admin-only", domain.AdminRoleModerator, []domain.AdminRole{domain.AdminRoleAdmin}, http.StatusForbidden},
		{"moderator on both", domain.AdminRoleModerator, []domain.AdminRole{domain.AdminRoleAdmin, domain.AdminRoleModerator}, http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			h := RequireAdminRole(tc.allowed...)(okHandler(&reached))

			req := httptest.NewRequest(http.MethodGet, "/admin/x", nil)
			ctx := context.WithValue(req.Context(), AdminKey, &domain.AdminUser{ID: 1, Role: tc.have})

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req.WithContext(ctx))

			require.Equal(t, tc.wantCode, rec.Code)
		})
	}
}

// Без админа в контексте RequireAdminRole обязан отказывать, а не падать —
// это защита от неверного порядка middleware.
func TestRequireAdminRoleWithoutAdminInContext(t *testing.T) {
	reached := false
	h := RequireAdminRole(domain.AdminRoleAdmin)(okHandler(&reached))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/x", nil))

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.False(t, reached)
}

func TestRequireSameOrigin(t *testing.T) {
	log := slog.New(slog.DiscardHandler)

	cases := []struct {
		name     string
		method   string
		origin   string
		wantCode int
	}{
		{"GET without origin allowed", http.MethodGet, "", http.StatusOK},
		{"POST same origin allowed", http.MethodPost, "https://api.meetuper.site", http.StatusOK},
		{"POST cross origin blocked", http.MethodPost, "https://evil.example", http.StatusForbidden},
		{"POST missing origin blocked", http.MethodPost, "", http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			h := RequireSameOrigin(log)(okHandler(&reached))

			req := httptest.NewRequest(tc.method, "https://api.meetuper.site/admin/x", nil)
			req.Host = "api.meetuper.site"
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			require.Equal(t, tc.wantCode, rec.Code)
		})
	}
}
