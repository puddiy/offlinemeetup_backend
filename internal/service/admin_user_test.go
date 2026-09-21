package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo/mocks"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"go.uber.org/mock/gomock"
)

type adminUserFixture struct {
	repo   *mocks.MockAdminUserRepository
	tokens *mocks.MockRefreshTokenRevoker
	audit  *recordingAuditSvc
	cache  *fakeProfileCache
	svc    *AdminUserService
}

func setupAdminUserTest(t *testing.T) *adminUserFixture {
	t.Helper()
	ctrl := gomock.NewController(t)

	f := &adminUserFixture{
		repo:   mocks.NewMockAdminUserRepository(ctrl),
		tokens: mocks.NewMockRefreshTokenRevoker(ctrl),
		audit:  &recordingAuditSvc{},
		cache:  &fakeProfileCache{},
	}
	f.svc = NewAdminUserService(f.repo, f.tokens, f.audit, f.cache, slog.New(slog.DiscardHandler))
	return f
}

func userWithProfile(id int64, email, username, display string, status domain.UserStatus) domain.User {
	return domain.User{
		ID:        id,
		Email:     email,
		Status:    status,
		CreatedAt: time.Now().UTC(),
		Profile:   &domain.Profile{UserID: id, Username: username, DisplayName: &display},
	}
}

func TestListUsersMapsRowsAndTotal(t *testing.T) {
	f := setupAdminUserTest(t)

	f.repo.EXPECT().
		List(gomock.Any(), gomock.Any()).
		Return([]domain.User{
			userWithProfile(1, "a@x.io", "alice", "Alice", domain.UserStatusActive),
			userWithProfile(2, "b@x.io", "bob", "Bob", domain.UserStatusBanned),
		}, 47, nil)

	page, err := f.svc.ListUsers(context.Background(), AdminUserFilter{Limit: 20, Offset: 20})

	require.NoError(t, err)
	require.Equal(t, 47, page.Total)
	require.Len(t, page.Items, 2)
	require.Equal(t, "alice", page.Items[0].Username)
	require.Equal(t, "Alice", page.Items[0].DisplayName)
	require.Equal(t, string(domain.UserStatusBanned), page.Items[1].Status)
	require.Equal(t, 3, page.Pages())
	require.Equal(t, 2, page.CurrentPage())
}

// Профиля может не быть (пользователь заведён, профиль не создан) —
// маппинг обязан это пережить, а не уронить весь список.
func TestListUsersSurvivesMissingProfile(t *testing.T) {
	f := setupAdminUserTest(t)

	f.repo.EXPECT().List(gomock.Any(), gomock.Any()).
		Return([]domain.User{{ID: 9, Email: "orphan@x.io", Status: domain.UserStatusActive}}, 1, nil)

	page, err := f.svc.ListUsers(context.Background(), AdminUserFilter{Limit: 20})

	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Empty(t, page.Items[0].Username)
	require.Empty(t, page.Items[0].DisplayName)
}

func TestListUsersClampsLimit(t *testing.T) {
	cases := []struct {
		name      string
		in        int
		wantLimit int
	}{
		{"ноль → дефолт", 0, defaultAdminUserLimit},
		{"отрицательный → дефолт", -5, defaultAdminUserLimit},
		{"выше потолка → потолок", 5000, maxAdminUserLimit},
		{"в пределах → как есть", 30, 30},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := setupAdminUserTest(t)

			var got repo.UserQuery
			f.repo.EXPECT().List(gomock.Any(), gomock.Any()).
				DoAndReturn(func(_ context.Context, q repo.UserQuery) ([]domain.User, int, error) {
					got = q
					return nil, 0, nil
				})

			page, err := f.svc.ListUsers(context.Background(), AdminUserFilter{Limit: tc.in})
			require.NoError(t, err)
			require.Equal(t, tc.wantLimit, got.Limit)
			require.Equal(t, tc.wantLimit, page.Limit)
		})
	}
}

func TestListUsersClampsNegativeOffset(t *testing.T) {
	f := setupAdminUserTest(t)

	var got repo.UserQuery
	f.repo.EXPECT().List(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, q repo.UserQuery) ([]domain.User, int, error) {
			got = q
			return nil, 0, nil
		})

	_, err := f.svc.ListUsers(context.Background(), AdminUserFilter{Offset: -100})
	require.NoError(t, err)
	require.Equal(t, 0, got.Offset)
}

func TestGetUserTranslatesNotFound(t *testing.T) {
	f := setupAdminUserTest(t)

	f.repo.EXPECT().GetDetail(gomock.Any(), int64(7)).Return(nil, repo.ErrUserNotFound)

	_, err := f.svc.GetUser(context.Background(), 7)
	require.ErrorIs(t, err, ErrNotFound, "репо-сентинел обязан транслироваться в сервисный")
}

func TestGetUserPropagatesUnknownError(t *testing.T) {
	f := setupAdminUserTest(t)
	boom := errors.New("db down")

	f.repo.EXPECT().GetDetail(gomock.Any(), int64(7)).Return(nil, boom)

	_, err := f.svc.GetUser(context.Background(), 7)
	require.ErrorIs(t, err, boom)
	require.NotErrorIs(t, err, ErrNotFound)
}

// recordingAuditSvc ловит записи журнала, сделанные сервисом.
type recordingAuditSvc struct {
	events []AuditEvent
	err    error
}

func (a *recordingAuditSvc) Record(_ context.Context, _ bun.IDB, ev AuditEvent) error {
	if a.err != nil {
		return a.err
	}
	a.events = append(a.events, ev)
	return nil
}

// fakeProfileCache считает инвалидации.
type fakeProfileCache struct{ invalidated []int64 }

func (c *fakeProfileCache) InvalidateProfile(_ context.Context, userID int64) error {
	c.invalidated = append(c.invalidated, userID)
	return nil
}

// fakeTx — минимальная заглушка bun.Tx для проверки, что мутация и запись
// журнала попали в ОДНУ транзакцию. RunInTx у мока просто исполняет
// замыкание, передавая нулевую bun.Tx.
func expectRunInTx(f *adminUserFixture) {
	f.repo.EXPECT().RunInTx(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, fn func(tx bun.Tx) error) error {
			return fn(bun.Tx{})
		})
}

func TestSetStatusBansAndAudits(t *testing.T) {
	f := setupAdminUserTest(t)

	expectRunInTx(f)
	f.repo.EXPECT().
		SetStatusTx(gomock.Any(), gomock.Any(), int64(42), domain.UserStatusBanned).
		Return(nil)

	err := f.svc.SetStatus(context.Background(), 7, 42, domain.UserStatusBanned, "10.0.0.1")

	require.NoError(t, err)
	require.Len(t, f.audit.events, 1)
	ev := f.audit.events[0]
	require.Equal(t, AuditActionUserBan, ev.Action)
	require.Equal(t, int64(7), ev.AdminID, "в журнале — АДМИН, а не жертва")
	require.Equal(t, "user", ev.TargetType)
	require.Equal(t, "42", ev.TargetID)
	require.Equal(t, "10.0.0.1", ev.IP)
	require.Equal(t, []int64{42}, f.cache.invalidated, "кэш профиля обязан сброситься")
}

func TestSetStatusUnbanUsesOwnAction(t *testing.T) {
	f := setupAdminUserTest(t)

	expectRunInTx(f)
	f.repo.EXPECT().SetStatusTx(gomock.Any(), gomock.Any(), int64(42), domain.UserStatusActive).Return(nil)

	require.NoError(t, f.svc.SetStatus(context.Background(), 7, 42, domain.UserStatusActive, ""))
	require.Equal(t, AuditActionUnbanOrBan(domain.UserStatusActive), f.audit.events[0].Action)
}

func TestSetStatusRejectsUnknownStatus(t *testing.T) {
	f := setupAdminUserTest(t)

	err := f.svc.SetStatus(context.Background(), 7, 42, domain.UserStatus("superuser"), "")

	require.ErrorIs(t, err, ErrInvalidInput)
	require.Empty(t, f.audit.events, "невалидный статус не должен доезжать до БД")
}

func TestSetStatusTranslatesNotFound(t *testing.T) {
	f := setupAdminUserTest(t)

	expectRunInTx(f)
	f.repo.EXPECT().SetStatusTx(gomock.Any(), gomock.Any(), int64(42), gomock.Any()).
		Return(repo.ErrUserNotFound)

	err := f.svc.SetStatus(context.Background(), 7, 42, domain.UserStatusBanned, "")

	require.ErrorIs(t, err, ErrNotFound)
	require.Empty(t, f.cache.invalidated, "провалившаяся мутация не инвалидирует кэш")
}

// Если мутация прошла, а журнал — нет, транзакция обязана откатиться целиком.
func TestSetStatusRollsBackWhenAuditFails(t *testing.T) {
	f := setupAdminUserTest(t)
	f.audit.err = errors.New("audit table gone")

	expectRunInTx(f)
	f.repo.EXPECT().SetStatusTx(gomock.Any(), gomock.Any(), int64(42), gomock.Any()).Return(nil)

	err := f.svc.SetStatus(context.Background(), 7, 42, domain.UserStatusBanned, "")

	require.Error(t, err)
	require.Empty(t, f.cache.invalidated)
}
