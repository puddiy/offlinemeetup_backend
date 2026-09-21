package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/config"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
	"github.com/stretchr/testify/require"
)

type stubUserSvc struct {
	gotFilter service.AdminUserFilter
	page      dto.Page[dto.AdminUserRow]
	err       error

	gotActor  int64
	gotUserID int64
	gotStatus domain.UserStatus
	gotIP     string
	statusErr error
	logoutErr error
}

func (s *stubUserSvc) SetStatus(_ context.Context, actorID, userID int64, status domain.UserStatus, ip string) error {
	s.gotActor, s.gotUserID, s.gotStatus, s.gotIP = actorID, userID, status, ip
	return s.statusErr
}

func (s *stubUserSvc) ListUsers(_ context.Context, f service.AdminUserFilter) (dto.Page[dto.AdminUserRow], error) {
	s.gotFilter = f
	return s.page, s.err
}

func newUsersHandler(t *testing.T, svc *stubUserSvc) *Handler {
	t.Helper()
	r, err := NewRenderer(slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	return NewHandler(&stubAuth{}, svc, &recordingAudit{}, r, &config.Config{Env: "local"}, slog.New(slog.DiscardHandler))
}

func usersRequest(target string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	admin := &domain.AdminUser{ID: 1, Email: "root@x.io", Role: domain.AdminRoleAdmin}
	return req.WithContext(context.WithValue(req.Context(), middleware.AdminKey, admin))
}

func TestUsersListRendersFullPage(t *testing.T) {
	svc := &stubUserSvc{page: dto.NewPage([]dto.AdminUserRow{
		{ID: 1, Email: "a@x.io", Username: "alice", Status: "active"},
	}, 1, 20, 0)}
	h := newUsersHandler(t, svc)

	rec := httptest.NewRecorder()
	h.UsersList(rec, usersRequest("/admin/users"))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "<!DOCTYPE html>")
	require.Contains(t, body, "a@x.io")
}

func TestUsersListHTMXReturnsPartialOnly(t *testing.T) {
	svc := &stubUserSvc{page: dto.NewPage([]dto.AdminUserRow{
		{ID: 1, Email: "a@x.io", Username: "alice", Status: "active"},
	}, 1, 20, 0)}
	h := newUsersHandler(t, svc)

	req := usersRequest("/admin/users")
	req.Header.Set("HX-Request", "true")

	rec := httptest.NewRecorder()
	h.UsersList(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "<!DOCTYPE html>")
	require.Contains(t, rec.Body.String(), "a@x.io")
}

func TestUsersListPassesFilters(t *testing.T) {
	svc := &stubUserSvc{}
	h := newUsersHandler(t, svc)

	rec := httptest.NewRecorder()
	h.UsersList(rec, usersRequest("/admin/users?q=alice&status=banned&deleted=1&limit=50&offset=100"))

	require.Equal(t, "alice", svc.gotFilter.Search)
	require.Equal(t, "banned", svc.gotFilter.Status)
	require.True(t, svc.gotFilter.OnlyDeleted)
	require.Equal(t, 50, svc.gotFilter.Limit)
	require.Equal(t, 100, svc.gotFilter.Offset)
}

func TestUsersListIgnoresGarbageNumbers(t *testing.T) {
	svc := &stubUserSvc{}
	h := newUsersHandler(t, svc)

	rec := httptest.NewRecorder()
	h.UsersList(rec, usersRequest("/admin/users?limit=abc&offset=xyz"))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 0, svc.gotFilter.Limit, "мусор → дефолт сервиса, не 400")
	require.Equal(t, 0, svc.gotFilter.Offset)
}

func TestUsersListServiceErrorShowsMessage(t *testing.T) {
	svc := &stubUserSvc{err: errors.New("db down")}
	h := newUsersHandler(t, svc)

	rec := httptest.NewRecorder()
	h.UsersList(rec, usersRequest("/admin/users"))

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Contains(t, rec.Body.String(), "Не удалось загрузить список")
}

// Пользовательский ввод из строки поиска обязан экранироваться.
func TestUsersListEscapesSearchInput(t *testing.T) {
	svc := &stubUserSvc{}
	h := newUsersHandler(t, svc)

	rec := httptest.NewRecorder()
	h.UsersList(rec, usersRequest("/admin/users?q=%3Cscript%3Ealert(1)%3C%2Fscript%3E"))

	require.NotContains(t, rec.Body.String(), "<script>alert(1)</script>")
}

func postWithChiParam(target, id string, admin *domain.AdminUser) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	if admin != nil {
		ctx = context.WithValue(ctx, middleware.AdminKey, admin)
	}
	return req.WithContext(ctx)
}

func TestUserBanCallsServiceAndRedirects(t *testing.T) {
	svc := &stubUserSvc{}
	h := newUsersHandler(t, svc)
	admin := &domain.AdminUser{ID: 7, Email: "root@x.io", Role: domain.AdminRoleAdmin}

	rec := httptest.NewRecorder()
	h.UserBan(rec, postWithChiParam("/admin/users/42/ban", "42", admin))

	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Contains(t, rec.Header().Get("Location"), "/admin/users/42")
	require.Equal(t, int64(7), svc.gotActor)
	require.Equal(t, int64(42), svc.gotUserID)
	require.Equal(t, domain.UserStatusBanned, svc.gotStatus)
}

func TestUserUnbanSetsActive(t *testing.T) {
	svc := &stubUserSvc{}
	h := newUsersHandler(t, svc)
	admin := &domain.AdminUser{ID: 7, Role: domain.AdminRoleAdmin}

	rec := httptest.NewRecorder()
	h.UserUnban(rec, postWithChiParam("/admin/users/42/unban", "42", admin))

	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, domain.UserStatusActive, svc.gotStatus)
}

func TestUserBanWithoutAdminRedirectsToLogin(t *testing.T) {
	svc := &stubUserSvc{}
	h := newUsersHandler(t, svc)

	rec := httptest.NewRecorder()
	h.UserBan(rec, postWithChiParam("/admin/users/42/ban", "42", nil))

	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, loginPath, rec.Header().Get("Location"))
	require.Zero(t, svc.gotUserID, "сервис не должен вызываться без админа в контексте")
}

func TestUserBanServiceErrorShowsMessage(t *testing.T) {
	svc := &stubUserSvc{statusErr: errors.New("db down")}
	h := newUsersHandler(t, svc)
	admin := &domain.AdminUser{ID: 7, Role: domain.AdminRoleAdmin}

	rec := httptest.NewRecorder()
	h.UserBan(rec, postWithChiParam("/admin/users/42/ban", "42", admin))

	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Contains(t, rec.Header().Get("Location"), "err=")
}

func (s *stubUserSvc) LogoutEverywhere(_ context.Context, actorID, userID int64, ip string) error {
	s.gotActor, s.gotUserID, s.gotIP = actorID, userID, ip
	return s.logoutErr
}

func TestUserLogoutAllCallsService(t *testing.T) {
	svc := &stubUserSvc{}
	h := newUsersHandler(t, svc)

	rec := httptest.NewRecorder()
	h.UserLogoutAll(rec, postWithChiParam("/admin/users/42/logout-all", "42", &domain.AdminUser{ID: 7, Role: domain.AdminRoleAdmin}))

	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Contains(t, rec.Header().Get("Location"), "flash=")
	require.Equal(t, int64(7), svc.gotActor)
	require.Equal(t, int64(42), svc.gotUserID)
}

func TestUserLogoutAllErrorShowsMessage(t *testing.T) {
	svc := &stubUserSvc{logoutErr: errors.New("redis down")}
	h := newUsersHandler(t, svc)

	rec := httptest.NewRecorder()
	h.UserLogoutAll(rec, postWithChiParam("/admin/users/42/logout-all", "42", &domain.AdminUser{ID: 7, Role: domain.AdminRoleAdmin}))

	require.Contains(t, rec.Header().Get("Location"), "err=")
}
