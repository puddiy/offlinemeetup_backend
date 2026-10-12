package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/cache"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo/mocks"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func statsMoscow(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Moscow")
	require.NoError(t, err)
	return loc
}

// setupAdminStatsTest — сервис с моком репозитория и НАСТОЯЩИМ кешем на
// miniredis: тесты кеширования проверяют поведение cache.Load, а не мок.
func setupAdminStatsTest(t *testing.T, now time.Time) (*AdminStatsService, *mocks.MockStatsRepository) {
	t.Helper()
	ctrl := gomock.NewController(t)
	r := mocks.NewMockStatsRepository(ctrl)

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	c := cache.NewAdminStatsCache(cache.NewRedisCache(rdb, slog.New(slog.DiscardHandler)), cache.NopMetrics, time.Minute)

	svc := NewAdminStatsService(r, c, statsMoscow(t))
	svc.now = func() time.Time { return now }
	return svc, r
}

// expectLive — дешёвая часть дашборда, читается на каждый показ.
func expectLive(r *mocks.MockStatsRepository, times int) {
	r.EXPECT().OpenReports(gomock.Any()).Return(repo.OpenReportsSummary{}, nil).Times(times)
	r.EXPECT().RecentAudit(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(times)
}

func TestAdminStatsDashboardFillsThirtyDays(t *testing.T) {
	now := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC) // 12:00 по Москве
	svc, r := setupAdminStatsTest(t, now)

	r.EXPECT().Totals(gomock.Any(), gomock.Any()).Return(repo.StatsTotals{UsersTotal: 42, Active1d: 7}, nil)
	r.EXPECT().DailyCounts(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, s repo.StatsSeries, _ time.Time, _ string) ([]repo.DayCount, error) {
			if s != repo.StatsSeriesRegistrations {
				return nil, nil
			}
			return []repo.DayCount{
				{Day: "2026-10-10", Count: 3},
				{Day: "2026-10-01", Count: 2},
				{Day: "2026-09-01", Count: 9}, // вне окна — не считается
			}, nil
		}).Times(3)
	expectLive(r, 1)

	d, err := svc.Dashboard(context.Background())
	require.NoError(t, err)

	reg := d.Snapshot.Registrations
	require.Len(t, reg.Points, 30)
	require.Equal(t, "2026-09-11", reg.Points[0].Date)
	require.Equal(t, "2026-10-10", reg.Points[29].Date)
	require.Equal(t, 3, reg.Points[29].Count)
	require.Equal(t, 2, reg.Points[20].Count) // 2026-10-01
	require.Equal(t, 5, reg.Total)
	require.Equal(t, 3, reg.Max)

	require.Len(t, d.Snapshot.Messages.Points, 30, "пустой ряд — тоже 30 нулевых дней")
	require.Equal(t, 0, d.Snapshot.Messages.Max)

	require.Equal(t, 42, d.Snapshot.UsersTotal)
	require.Equal(t, 7, d.Snapshot.Active1d)
	require.True(t, d.Snapshot.GeneratedAt.Equal(now))
}

func TestAdminStatsWindowFollowsAdminTimezone(t *testing.T) {
	msk := statsMoscow(t)
	// 22:30 UTC 10 октября — это уже 01:30 11 октября по Москве. Время
	// подаётся в московском поясе: так выглядит time.Now() приложения,
	// запущенного не в UTC.
	now := time.Date(2026, 10, 10, 22, 30, 0, 0, time.UTC)
	svc, r := setupAdminStatsTest(t, now.In(msk))
	wantSince := time.Date(2026, 9, 12, 0, 0, 0, 0, msk).UTC() // 2026-09-11 21:00 UTC

	r.EXPECT().Totals(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, got time.Time) (repo.StatsTotals, error) {
			require.Equal(t, time.UTC, got.Location(), "в SQL уходит только UTC")
			require.True(t, got.Equal(now))
			return repo.StatsTotals{}, nil
		})
	r.EXPECT().DailyCounts(gomock.Any(), gomock.Any(), gomock.Any(), "Europe/Moscow").
		DoAndReturn(func(_ context.Context, _ repo.StatsSeries, since time.Time, _ string) ([]repo.DayCount, error) {
			require.Equal(t, time.UTC, since.Location(), "в SQL уходит только UTC")
			require.True(t, since.Equal(wantSince), "since = %s, want %s", since, wantSince)
			return nil, nil
		}).Times(3)
	expectLive(r, 1)

	d, err := svc.Dashboard(context.Background())
	require.NoError(t, err)

	pts := d.Snapshot.Registrations.Points
	require.Equal(t, "2026-09-12", pts[0].Date)
	require.Equal(t, "2026-10-11", pts[29].Date, "последний столбец — «сегодня» по Москве, а не по UTC")
	require.Equal(t, time.UTC, d.Snapshot.GeneratedAt.Location())
}

func TestAdminStatsSnapshotCachedLiveFresh(t *testing.T) {
	svc, r := setupAdminStatsTest(t, time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC))

	// Тяжёлая часть — один раз на два показа.
	r.EXPECT().Totals(gomock.Any(), gomock.Any()).Return(repo.StatsTotals{UsersTotal: 1}, nil).Times(1)
	r.EXPECT().DailyCounts(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(3)
	// Живая — на каждый показ: между показами модератор закрыл жалобу.
	r.EXPECT().OpenReports(gomock.Any()).Return(repo.OpenReportsSummary{Count: 2}, nil)
	r.EXPECT().OpenReports(gomock.Any()).Return(repo.OpenReportsSummary{Count: 1}, nil)
	r.EXPECT().RecentAudit(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(2)

	ctx := context.Background()
	first, err := svc.Dashboard(ctx)
	require.NoError(t, err)
	second, err := svc.Dashboard(ctx)
	require.NoError(t, err)

	require.Equal(t, 2, first.OpenReports)
	require.Equal(t, 1, second.OpenReports, "жалобы читаются вживую, мимо кеша")
	require.Equal(t, 1, second.Snapshot.UsersTotal, "снапшот пришёл из кеша")
}

func TestAdminStatsFeedSkipsSessionEvents(t *testing.T) {
	svc, r := setupAdminStatsTest(t, time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC))
	at := time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)

	r.EXPECT().Totals(gomock.Any(), gomock.Any()).Return(repo.StatsTotals{}, nil)
	r.EXPECT().DailyCounts(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(3)
	r.EXPECT().OpenReports(gomock.Any()).Return(repo.OpenReportsSummary{Count: 4, OldestAt: &at}, nil)
	r.EXPECT().RecentAudit(gomock.Any(), 10, []string{AuditActionLogin, AuditActionLogout}).
		Return([]repo.AuditFeedRow{{
			ID: 5, AdminEmail: "mod@x.io", Action: AuditActionMeetupCancel,
			TargetType: "meetup", TargetID: "15", CreatedAt: at,
		}}, nil)

	d, err := svc.Dashboard(context.Background())
	require.NoError(t, err)

	require.Equal(t, 4, d.OpenReports)
	require.NotNil(t, d.OldestOpenReportAt)
	require.True(t, d.OldestOpenReportAt.Equal(at))
	require.Len(t, d.RecentActions, 1)
	a := d.RecentActions[0]
	require.Equal(t, int64(5), a.ID)
	require.Equal(t, "mod@x.io", a.AdminEmail)
	require.Equal(t, AuditActionMeetupCancel, a.Action)
	require.Equal(t, "meetup", a.TargetType)
	require.Equal(t, "15", a.TargetID)
	require.True(t, a.CreatedAt.Equal(at))
}

func TestAdminStatsErrorIsNotCached(t *testing.T) {
	svc, r := setupAdminStatsTest(t, time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC))
	ctx := context.Background()
	boom := errors.New("db down")

	r.EXPECT().Totals(gomock.Any(), gomock.Any()).Return(repo.StatsTotals{}, boom)
	_, err := svc.Dashboard(ctx)
	require.ErrorIs(t, err, boom)

	// Следующий показ обязан пойти в БД снова, а не получить закешированный провал.
	r.EXPECT().Totals(gomock.Any(), gomock.Any()).Return(repo.StatsTotals{UsersTotal: 3}, nil)
	r.EXPECT().DailyCounts(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(3)
	expectLive(r, 1)

	d, err := svc.Dashboard(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, d.Snapshot.UsersTotal)
}

func TestAdminStatsLiveErrorFails(t *testing.T) {
	svc, r := setupAdminStatsTest(t, time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC))
	boom := errors.New("db down")

	r.EXPECT().Totals(gomock.Any(), gomock.Any()).Return(repo.StatsTotals{}, nil)
	r.EXPECT().DailyCounts(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(3)
	r.EXPECT().OpenReports(gomock.Any()).Return(repo.OpenReportsSummary{}, boom)

	_, err := svc.Dashboard(context.Background())
	require.ErrorIs(t, err, boom)
}
