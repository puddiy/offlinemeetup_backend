package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

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

	user      *domain.User
	getErr    error
	deleteErr error
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

func (s *stubUserSvc) GetUser(_ context.Context, id int64) (*domain.User, error) {
	s.gotUserID = id
	return s.user, s.getErr
}

func (s *stubUserSvc) DeleteByAdmin(_ context.Context, actorID, userID int64, ip string) error {
	s.gotActor, s.gotUserID, s.gotIP = actorID, userID, ip
	return s.deleteErr
}

func getWithChiParam(target, id string, admin *domain.AdminUser) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	if admin != nil {
		ctx = context.WithValue(ctx, middleware.AdminKey, admin)
	}
	return req.WithContext(ctx)
}

func TestUserDetailRenders(t *testing.T) {
	display := "Alice"
	svc := &stubUserSvc{user: &domain.User{
		ID: 42, Email: "a@x.io", Status: domain.UserStatusActive,
		Profile: &domain.Profile{UserID: 42, Username: "alice", DisplayName: &display},
	}}
	h := newUsersHandler(t, svc)

	rec := httptest.NewRecorder()
	h.UserDetail(rec, getWithChiParam("/admin/users/42", "42", &domain.AdminUser{ID: 7, Role: domain.AdminRoleAdmin}))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "a@x.io")
	require.Contains(t, body, "Заблокировать")
	require.Contains(t, body, "Удалить аккаунт")
}

// У пользователя может не быть профиля — карточка обязана отрисоваться,
// а не упасть на разыменовании nil.
func TestUserDetailWithoutProfile(t *testing.T) {
	svc := &stubUserSvc{user: &domain.User{ID: 42, Email: "a@x.io", Status: domain.UserStatusActive}}
	h := newUsersHandler(t, svc)

	rec := httptest.NewRecorder()
	h.UserDetail(rec, getWithChiParam("/admin/users/42", "42", &domain.AdminUser{ID: 7, Role: domain.AdminRoleAdmin}))

	require.Equal(t, http.StatusOK, rec.Code)
}

// У удалённого нет кнопок действий — иначе поддержка «разбанит» аккаунт,
// в который всё равно невозможно войти.
func TestUserDetailDeletedHasNoActions(t *testing.T) {
	now := time.Now()
	svc := &stubUserSvc{user: &domain.User{ID: 42, Status: domain.UserStatusInactive, DeletedAt: &now}}
	h := newUsersHandler(t, svc)

	rec := httptest.NewRecorder()
	h.UserDetail(rec, getWithChiParam("/admin/users/42", "42", &domain.AdminUser{ID: 7, Role: domain.AdminRoleAdmin}))

	body := rec.Body.String()
	require.Contains(t, body, "удалён")
	require.NotContains(t, body, "Заблокировать")
	require.NotContains(t, body, "Удалить аккаунт")
}

func TestUserDetailNotFound(t *testing.T) {
	svc := &stubUserSvc{getErr: service.ErrNotFound}
	h := newUsersHandler(t, svc)

	rec := httptest.NewRecorder()
	h.UserDetail(rec, getWithChiParam("/admin/users/42", "42", &domain.AdminUser{ID: 7, Role: domain.AdminRoleAdmin}))

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "Пользователь не найден")
}

func TestUserDeleteSuccess(t *testing.T) {
	svc := &stubUserSvc{}
	h := newUsersHandler(t, svc)

	rec := httptest.NewRecorder()
	h.UserDelete(rec, postWithChiParam("/admin/users/42/delete", "42", &domain.AdminUser{ID: 7, Role: domain.AdminRoleAdmin}))

	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Contains(t, rec.Header().Get("Location"), "flash=")
	require.Equal(t, int64(42), svc.gotUserID)
}

func TestUserDeleteAlreadyDeleted(t *testing.T) {
	svc := &stubUserSvc{deleteErr: service.ErrUserAlreadyDeleted}
	h := newUsersHandler(t, svc)

	rec := httptest.NewRecorder()
	h.UserDelete(rec, postWithChiParam("/admin/users/42/delete", "42", &domain.AdminUser{ID: 7, Role: domain.AdminRoleAdmin}))

	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Contains(t, rec.Header().Get("Location"), "err=")
}

// Находка ревью: ссылки пагинации собирались как href="/admin/users?{{...}}",
// и html/template экранировал разделители запроса («&» → «%26», «=» → «%3d»),
// из-за чего весь набор параметров приезжал обратно ОДНИМ именем с пустым
// значением. Поддержка не могла уйти со страницы 1, а фильтр молча терялся.
// Тесты этого не ловили: во всех фикстурах total <= limit, то есть ветки со
// ссылками ни разу не рендерились.
func TestUsersTableLinksAreNotOverEscaped(t *testing.T) {
	r, err := NewRenderer(slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	r.RenderPartial(rec, http.StatusOK, "users_table", UsersPageData{
		Search: "alice",
		Status: "banned",
		Page:   dto.NewPage([]dto.AdminUserRow{{ID: 1}}, 47, 20, 20),
	})

	body := rec.Body.String()
	require.NotContains(t, body, "%26", "«&» не должен быть процентно-закодирован")
	require.NotContains(t, body, "%3d", "«=» не должен быть процентно-закодирован")
	require.Contains(t, body, "offset=40", "ссылка «вперёд» обязана нести следующий offset")
	require.Contains(t, body, "offset=0", "ссылка «назад» обязана нести предыдущий offset")
	require.Contains(t, body, "q=alice", "активный поиск обязан переживать переход по страницам")
	require.Contains(t, body, "status=banned", "активный фильтр статуса — тоже")
}

// Кнопка удаления не должна показываться модератору: маршрут всё равно
// вернёт ему 403, а предлагать недоступное действие — плохой интерфейс.
func TestUserDetailHidesDeleteFromModerator(t *testing.T) {
	display := "Alice"
	user := &domain.User{
		ID: 42, Email: "a@x.io", Status: domain.UserStatusActive,
		Profile: &domain.Profile{UserID: 42, Username: "alice", DisplayName: &display},
	}

	cases := []struct {
		role    domain.AdminRole
		wantBtn bool
	}{
		{domain.AdminRoleAdmin, true},
		{domain.AdminRoleModerator, false},
	}

	for _, tc := range cases {
		t.Run(string(tc.role), func(t *testing.T) {
			h := newUsersHandler(t, &stubUserSvc{user: user})

			rec := httptest.NewRecorder()
			h.UserDetail(rec, getWithChiParam("/admin/users/42", "42",
				&domain.AdminUser{ID: 7, Email: "root@x.io", Role: tc.role}))

			require.Equal(t, http.StatusOK, rec.Code)
			body := rec.Body.String()
			require.Contains(t, body, "Заблокировать", "бан доступен обеим ролям")
			if tc.wantBtn {
				require.Contains(t, body, "Удалить аккаунт")
			} else {
				require.NotContains(t, body, "Удалить аккаунт")
			}
		})
	}
}

// Текст плашек не должен приходить из URL: иначе ссылка на НАСТОЯЩИЙ домен
// панели с ?flash=«Сессия истекла, позвоните …» показывала бы модератору
// поддельное «официальное» сообщение. XSS тут нет (html/template экранирует),
// но для фишинга хватает и текста. Неизвестный ключ не рисуется вовсе.
func TestUserDetailIgnoresArbitraryNoticeText(t *testing.T) {
	svc := &stubUserSvc{user: &domain.User{ID: 42, Email: "a@x.io", Status: domain.UserStatusActive}}
	h := newUsersHandler(t, svc)

	const spoofFlash = "Сессия истекла. Позвоните +7-000"
	const spoofErr = "Введите пароль повторно на evil.example"
	target := "/admin/users/42?" + url.Values{"flash": {spoofFlash}, "err": {spoofErr}}.Encode()

	rec := httptest.NewRecorder()
	h.UserDetail(rec, getWithChiParam(target, "42", &domain.AdminUser{ID: 7, Role: domain.AdminRoleAdmin}))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.NotContains(t, body, spoofFlash)
	require.NotContains(t, body, spoofErr)
	require.NotContains(t, body, `class="flash"`)
	require.NotContains(t, body, `class="error"`)
}

// Ключ успеха не должен рисоваться в красной плашке ошибки, и наоборот:
// ?err=banned показал бы «Пользователь заблокирован» как ошибку.
func TestUserDetailNoticeKeysDoNotCrossBanners(t *testing.T) {
	svc := &stubUserSvc{user: &domain.User{ID: 42, Email: "a@x.io", Status: domain.UserStatusActive}}
	h := newUsersHandler(t, svc)

	rec := httptest.NewRecorder()
	h.UserDetail(rec, getWithChiParam("/admin/users/42?err=banned&flash=delete_failed", "42",
		&domain.AdminUser{ID: 7, Role: domain.AdminRoleAdmin}))

	body := rec.Body.String()
	require.NotContains(t, body, `class="flash"`)
	require.NotContains(t, body, `class="error"`)
}

// Сквозная проверка каталога: каждое действие редиректит с ключом, и карточка
// по этому Location показывает ожидаемый текст в нужной плашке. Ловит ключ,
// для которого забыли завести текст — иначе плашка молча пропала бы.
func TestUserActionNoticesRoundTrip(t *testing.T) {
	admin := &domain.AdminUser{ID: 7, Role: domain.AdminRoleAdmin}
	boom := errors.New("boom")

	cases := []struct {
		name   string
		svc    *stubUserSvc
		action func(h *Handler, w http.ResponseWriter, r *http.Request)
		path   string
		banner string
		text   string
	}{
		{"ban", &stubUserSvc{}, (*Handler).UserBan, "ban", `class="flash"`, "Пользователь заблокирован"},
		{"unban", &stubUserSvc{}, (*Handler).UserUnban, "unban", `class="flash"`, "Блокировка снята"},
		{"ban failed", &stubUserSvc{statusErr: boom}, (*Handler).UserBan, "ban", `class="error"`, "Не удалось изменить статус"},
		{"logout-all", &stubUserSvc{}, (*Handler).UserLogoutAll, "logout-all", `class="flash"`, "Сессии отозваны"},
		{"logout-all failed", &stubUserSvc{logoutErr: boom}, (*Handler).UserLogoutAll, "logout-all", `class="error"`, "Не удалось отозвать сессии"},
		{"delete", &stubUserSvc{}, (*Handler).UserDelete, "delete", `class="flash"`, "Аккаунт удалён и анонимизирован"},
		{"delete twice", &stubUserSvc{deleteErr: service.ErrUserAlreadyDeleted}, (*Handler).UserDelete, "delete", `class="error"`, "Аккаунт уже был удалён"},
		{"delete failed", &stubUserSvc{deleteErr: boom}, (*Handler).UserDelete, "delete", `class="error"`, "Не удалось удалить аккаунт"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newUsersHandler(t, tc.svc)

			rec := httptest.NewRecorder()
			tc.action(h, rec, postWithChiParam("/admin/users/42/"+tc.path, "42", admin))
			require.Equal(t, http.StatusSeeOther, rec.Code)
			location := rec.Header().Get("Location")

			tc.svc.user = &domain.User{ID: 42, Email: "a@x.io", Status: domain.UserStatusActive}
			rec = httptest.NewRecorder()
			h.UserDetail(rec, getWithChiParam(location, "42", admin))

			body := rec.Body.String()
			require.Contains(t, body, tc.banner, location)
			require.Contains(t, body, tc.text, location)
		})
	}
}
