package service

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/cache"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/config"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo/mocks"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.org/x/crypto/bcrypt"
)

type adminAuthFixture struct {
	mr       *miniredis.Miniredis
	repo     *mocks.MockAdminRepository
	sessions *cache.RedisAdminSessionStore
	svc      *AdminAuthService
	cfg      *config.Config
}

func setupAdminAuthTest(t *testing.T) *adminAuthFixture {
	t.Helper()

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	log := slog.New(slog.DiscardHandler)

	ctrl := gomock.NewController(t)
	mockRepo := mocks.NewMockAdminRepository(ctrl)
	sessions := cache.NewRedisAdminSessionStore(rdb, log)

	cfg := &config.Config{
		AdminSessionTTL:    time.Hour,
		AdminSessionMaxTTL: 4 * time.Hour,
	}

	return &adminAuthFixture{
		mr:       mr,
		repo:     mockRepo,
		sessions: sessions,
		svc:      NewAdminAuthService(mockRepo, sessions, cfg, log),
		cfg:      cfg,
	}
}

// activeAdmin собирает админа с настоящим bcrypt-хэшем указанного пароля.
func activeAdmin(t *testing.T, id int64, email, password string, role domain.AdminRole) *domain.AdminUser {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	require.NoError(t, err)
	return &domain.AdminUser{
		ID:           id,
		Email:        email,
		PasswordHash: string(h),
		Role:         role,
		IsActive:     true,
	}
}

func TestAdminLoginSuccess(t *testing.T) {
	f := setupAdminAuthTest(t)
	defer f.mr.Close()

	admin := activeAdmin(t, 1, "a@x.io", "correct-horse", domain.AdminRoleAdmin)
	f.repo.EXPECT().GetByEmail(gomock.Any(), "a@x.io").Return(admin, nil)

	token, got, err := f.svc.Login(context.Background(), "a@x.io", "correct-horse")
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.Equal(t, int64(1), got.ID)
}

func TestAdminLoginWrongPassword(t *testing.T) {
	f := setupAdminAuthTest(t)
	defer f.mr.Close()

	admin := activeAdmin(t, 1, "a@x.io", "correct-horse", domain.AdminRoleAdmin)
	f.repo.EXPECT().GetByEmail(gomock.Any(), "a@x.io").Return(admin, nil)

	_, _, err := f.svc.Login(context.Background(), "a@x.io", "wrong")
	require.ErrorIs(t, err, ErrUnauthorized)
}

// Неизвестный email обязан давать ТОТ ЖЕ сентинел, что неверный пароль —
// иначе форма входа становится оракулом существования учёток.
func TestAdminLoginUnknownEmailSameError(t *testing.T) {
	f := setupAdminAuthTest(t)
	defer f.mr.Close()

	f.repo.EXPECT().GetByEmail(gomock.Any(), "ghost@x.io").Return(nil, repo.ErrAdminNotFound)

	_, _, err := f.svc.Login(context.Background(), "ghost@x.io", "whatever")
	require.ErrorIs(t, err, ErrUnauthorized)
}

func TestAdminLoginInactive(t *testing.T) {
	f := setupAdminAuthTest(t)
	defer f.mr.Close()

	admin := activeAdmin(t, 1, "a@x.io", "correct-horse", domain.AdminRoleAdmin)
	admin.IsActive = false
	f.repo.EXPECT().GetByEmail(gomock.Any(), "a@x.io").Return(admin, nil)

	_, _, err := f.svc.Login(context.Background(), "a@x.io", "correct-horse")
	require.ErrorIs(t, err, ErrUnauthorized)
}

func TestAdminAuthenticateSuccess(t *testing.T) {
	f := setupAdminAuthTest(t)
	defer f.mr.Close()

	admin := activeAdmin(t, 1, "a@x.io", "correct-horse", domain.AdminRoleAdmin)
	f.repo.EXPECT().GetByEmail(gomock.Any(), "a@x.io").Return(admin, nil)
	token, _, err := f.svc.Login(context.Background(), "a@x.io", "correct-horse")
	require.NoError(t, err)

	f.repo.EXPECT().GetByID(gomock.Any(), int64(1)).Return(admin, nil)
	got, err := f.svc.Authenticate(context.Background(), token)
	require.NoError(t, err)
	require.Equal(t, int64(1), got.ID)
	require.Equal(t, domain.AdminRoleAdmin, got.Role)
}

func TestAdminAuthenticateUnknownToken(t *testing.T) {
	f := setupAdminAuthTest(t)
	defer f.mr.Close()

	_, err := f.svc.Authenticate(context.Background(), "not-a-real-token")
	require.ErrorIs(t, err, ErrUnauthorized)
}

func TestAdminAuthenticateEmptyToken(t *testing.T) {
	f := setupAdminAuthTest(t)
	defer f.mr.Close()

	_, err := f.svc.Authenticate(context.Background(), "")
	require.ErrorIs(t, err, ErrUnauthorized)
}

// Деактивация админа обязана действовать НЕМЕДЛЕННО, а не по истечении TTL:
// поэтому Authenticate перечитывает строку на каждом запросе.
func TestAdminAuthenticateDeactivatedMidSession(t *testing.T) {
	f := setupAdminAuthTest(t)
	defer f.mr.Close()

	admin := activeAdmin(t, 1, "a@x.io", "correct-horse", domain.AdminRoleAdmin)
	f.repo.EXPECT().GetByEmail(gomock.Any(), "a@x.io").Return(admin, nil)
	token, _, err := f.svc.Login(context.Background(), "a@x.io", "correct-horse")
	require.NoError(t, err)

	deactivated := *admin
	deactivated.IsActive = false
	f.repo.EXPECT().GetByID(gomock.Any(), int64(1)).Return(&deactivated, nil)

	_, err = f.svc.Authenticate(context.Background(), token)
	require.ErrorIs(t, err, ErrUnauthorized)
}

// Абсолютный потолок бьёт даже по живой, регулярно продлеваемой сессии.
//
// Сессия кладётся напрямую, с IssuedAt в прошлом, а не через Login +
// miniredis.FastForward: FastForward двигает только TTL ключей Redis, а
// IssuedAt сравнивается с настенным временем — через Login этот тест
// проходил бы всегда, ничего не проверяя.
func TestAdminAuthenticateAbsoluteCap(t *testing.T) {
	f := setupAdminAuthTest(t)
	defer f.mr.Close()

	ctx := context.Background()
	const token = "token-from-a-long-lived-session"

	// TTL простоя — час (ключ жив), потолок — 4 часа (вход был 5 часов назад).
	require.NoError(t, f.sessions.Save(ctx, hashSessionToken(token), cache.AdminSession{
		AdminID:  1,
		Role:     string(domain.AdminRoleAdmin),
		IssuedAt: time.Now().UTC().Add(-5 * time.Hour),
	}, time.Hour))

	// GetByID не ожидаем: проверка потолка обязана отсечь раньше похода в БД.
	_, err := f.svc.Authenticate(ctx, token)
	require.ErrorIs(t, err, ErrUnauthorized)

	// И сессия должна быть подчищена, а не дожидаться своего TTL.
	_, found, err := f.sessions.Get(ctx, hashSessionToken(token))
	require.NoError(t, err)
	require.False(t, found)
}

func TestAdminLogoutRevokesSession(t *testing.T) {
	f := setupAdminAuthTest(t)
	defer f.mr.Close()

	admin := activeAdmin(t, 1, "a@x.io", "correct-horse", domain.AdminRoleAdmin)
	f.repo.EXPECT().GetByEmail(gomock.Any(), "a@x.io").Return(admin, nil)
	token, _, err := f.svc.Login(context.Background(), "a@x.io", "correct-horse")
	require.NoError(t, err)

	require.NoError(t, f.svc.Logout(context.Background(), token))

	_, err = f.svc.Authenticate(context.Background(), token)
	require.ErrorIs(t, err, ErrUnauthorized)
}

// Два входа подряд обязаны давать разные токены — иначе один общий секрет.
func TestAdminLoginTokensAreUnique(t *testing.T) {
	f := setupAdminAuthTest(t)
	defer f.mr.Close()

	admin := activeAdmin(t, 1, "a@x.io", "correct-horse", domain.AdminRoleAdmin)
	f.repo.EXPECT().GetByEmail(gomock.Any(), "a@x.io").Return(admin, nil).Times(2)

	t1, _, err := f.svc.Login(context.Background(), "a@x.io", "correct-horse")
	require.NoError(t, err)
	t2, _, err := f.svc.Login(context.Background(), "a@x.io", "correct-horse")
	require.NoError(t, err)

	require.NotEqual(t, t1, t2)
}
