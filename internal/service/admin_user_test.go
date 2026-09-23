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
	mcache *fakeMeetupCache
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
		mcache: &fakeMeetupCache{},
	}
	f.svc = NewAdminUserService(f.repo, f.tokens, f.audit, f.cache, f.mcache, slog.New(slog.DiscardHandler))
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

// fakeMeetupCache считает сброшенные снапшоты митапов.
type fakeMeetupCache struct{ invalidated []int64 }

func (c *fakeMeetupCache) InvalidateMeetup(_ context.Context, meetupID int64) error {
	c.invalidated = append(c.invalidated, meetupID)
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

func TestLogoutEverywhereRevokesAndAudits(t *testing.T) {
	f := setupAdminUserTest(t)

	f.repo.EXPECT().GetDetail(gomock.Any(), int64(42)).Return(&domain.User{ID: 42}, nil)
	f.tokens.EXPECT().RevokeAllForUser(gomock.Any(), int64(42)).Return(nil)
	expectRunInTx(f)

	err := f.svc.LogoutEverywhere(context.Background(), 7, 42, "10.0.0.1")

	require.NoError(t, err)
	require.Len(t, f.audit.events, 1)
	require.Equal(t, AuditActionUserLogout, f.audit.events[0].Action)
	require.Equal(t, int64(7), f.audit.events[0].AdminID)
	require.Equal(t, "42", f.audit.events[0].TargetID)
}

// Отзыв токенов живёт в своей таблице и своём репозитории — в одну
// транзакцию с журналом его не завести. Поэтому порядок такой: сначала
// отзываем, потом пишем журнал. Провал отзыва обязан остановить всё.
func TestLogoutEverywhereStopsOnRevokeFailure(t *testing.T) {
	f := setupAdminUserTest(t)
	boom := errors.New("redis down")

	f.repo.EXPECT().GetDetail(gomock.Any(), int64(42)).Return(&domain.User{ID: 42}, nil)
	f.tokens.EXPECT().RevokeAllForUser(gomock.Any(), int64(42)).Return(boom)

	err := f.svc.LogoutEverywhere(context.Background(), 7, 42, "")

	require.ErrorIs(t, err, boom)
	require.Empty(t, f.audit.events, "не записали в журнал то, чего не сделали")
}

func TestDeleteByAdminSoftDeletesAndAudits(t *testing.T) {
	f := setupAdminUserTest(t)

	expectRunInTx(f)
	f.repo.EXPECT().SoftDeleteTx(gomock.Any(), gomock.Any(), int64(42)).Return(nil)
	f.repo.EXPECT().MeetupIDsForUser(gomock.Any(), int64(42)).Return([]int64{11, 22}, nil)

	err := f.svc.DeleteByAdmin(context.Background(), 7, 42, "10.0.0.1")

	require.NoError(t, err)
	require.Len(t, f.audit.events, 1)
	require.Equal(t, AuditActionUserDelete, f.audit.events[0].Action)
	require.Equal(t, int64(7), f.audit.events[0].AdminID)
	require.Equal(t, "42", f.audit.events[0].TargetID)
	require.Equal(t, []int64{42}, f.cache.invalidated)
	require.Equal(t, []int64{11, 22}, f.mcache.invalidated,
		"снапшоты митапов держат Creator/Participants целиком — их тоже надо сбросить")
}

func TestDeleteByAdminTranslatesAlreadyDeleted(t *testing.T) {
	f := setupAdminUserTest(t)

	expectRunInTx(f)
	f.repo.EXPECT().SoftDeleteTx(gomock.Any(), gomock.Any(), int64(42)).
		Return(repo.ErrUserAlreadyDeleted)

	err := f.svc.DeleteByAdmin(context.Background(), 7, 42, "")

	require.ErrorIs(t, err, ErrUserAlreadyDeleted)
	require.Empty(t, f.cache.invalidated)
}

func TestDeleteByAdminTranslatesNotFound(t *testing.T) {
	f := setupAdminUserTest(t)

	expectRunInTx(f)
	f.repo.EXPECT().SoftDeleteTx(gomock.Any(), gomock.Any(), int64(42)).Return(repo.ErrUserNotFound)

	require.ErrorIs(t, f.svc.DeleteByAdmin(context.Background(), 7, 42, ""), ErrNotFound)
}

// Самоудаление — НЕ действие администратора: admin_audit_log.admin_id
// объявлен NOT NULL и означает именно админа. Пишем только в slog.
func TestDeleteOwnAccountDoesNotTouchAdminAudit(t *testing.T) {
	f := setupAdminUserTest(t)

	expectRunInTx(f)
	f.repo.EXPECT().SoftDeleteTx(gomock.Any(), gomock.Any(), int64(42)).Return(nil)
	f.repo.EXPECT().MeetupIDsForUser(gomock.Any(), int64(42)).Return([]int64{11}, nil)

	err := f.svc.DeleteOwnAccount(context.Background(), 42)

	require.NoError(t, err)
	require.Empty(t, f.audit.events, "в admin_audit_log самоудаление не пишется")
	require.Equal(t, []int64{42}, f.cache.invalidated)
	require.Equal(t, []int64{11}, f.mcache.invalidated)
}

func TestDeleteOwnAccountRejectsZeroID(t *testing.T) {
	f := setupAdminUserTest(t)
	require.ErrorIs(t, f.svc.DeleteOwnAccount(context.Background(), 0), ErrInvalidInput)
}

// Находка ревью: LogoutEverywhere рапортовал успех и писал в журнал для
// несуществующего пользователя. RevokeAllForUser по неизвестному id обновляет
// ноль строк и ошибки не возвращает, поэтому заход на
// /admin/users/999999/logout-all давал зелёный флеш и запись user.logout_all
// о действии, которого не было.
func TestLogoutEverywhereUnknownUser(t *testing.T) {
	f := setupAdminUserTest(t)

	f.repo.EXPECT().GetDetail(gomock.Any(), int64(999999)).Return(nil, repo.ErrUserNotFound)

	err := f.svc.LogoutEverywhere(context.Background(), 7, 999999, "10.0.0.1")

	require.ErrorIs(t, err, ErrNotFound)
	require.Empty(t, f.audit.events, "журнал не должен получить запись о действии над несуществующим пользователем")
}

// Находка ревью: offset не был ограничен сверху, хотя для митапов правило
// уже есть (maxMeetupOffset). ?offset=999999999 заставлял Postgres просеять
// и выбросить миллиард строк.
func TestListUsersClampsHugeOffset(t *testing.T) {
	f := setupAdminUserTest(t)

	var got repo.UserQuery
	f.repo.EXPECT().List(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, q repo.UserQuery) ([]domain.User, int, error) {
			got = q
			return nil, 0, nil
		})

	page, err := f.svc.ListUsers(context.Background(), AdminUserFilter{Offset: 999_999_999})

	require.NoError(t, err)
	require.Equal(t, maxAdminUserOffset, got.Offset)
	require.Equal(t, maxAdminUserOffset, page.Offset, "конверт обязан отдавать зажатый offset, иначе ссылки пагинации уведут обратно за потолок")
}

// Ошибка листинга митапов не должна отменять уже совершённое удаление:
// аккаунт удалён, откатывать нечего, профиль сброшен.
func TestDeleteSurvivesMeetupLookupFailure(t *testing.T) {
	f := setupAdminUserTest(t)

	expectRunInTx(f)
	f.repo.EXPECT().SoftDeleteTx(gomock.Any(), gomock.Any(), int64(42)).Return(nil)
	f.repo.EXPECT().MeetupIDsForUser(gomock.Any(), int64(42)).Return(nil, errors.New("db down"))

	require.NoError(t, f.svc.DeleteOwnAccount(context.Background(), 42))
	require.Equal(t, []int64{42}, f.cache.invalidated)
	require.Empty(t, f.mcache.invalidated)
}
