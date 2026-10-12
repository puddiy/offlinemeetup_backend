package service

import (
	"context"
	"fmt"
	"time"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo"
)

// dashboardDays — длина дневных рядов на дашборде. Ось графика в admin.css
// (.chart-axis) рассчитана ровно на это число колонок.
const dashboardDays = 30

// recentActionsLimit — сколько последних действий показывает дашборд.
const recentActionsLimit = 10

// dashboardFeedExcluded — действия, которых нет в ленте дашборда: входы и
// выходы админов забили бы её целиком, не неся ни одного решения.
var dashboardFeedExcluded = []string{AuditActionLogin, AuditActionLogout}

// StatsRepository — то, что дашборд требует от *repo.StatsRepo.
type StatsRepository interface {
	Totals(ctx context.Context, now time.Time) (repo.StatsTotals, error)
	DailyCounts(ctx context.Context, series repo.StatsSeries, since time.Time, tz string) ([]repo.DayCount, error)
	OpenReports(ctx context.Context) (repo.OpenReportsSummary, error)
	RecentAudit(ctx context.Context, limit int, excludeActions []string) ([]repo.AuditFeedRow, error)
}

// DashboardCache — кеш тяжёлой части дашборда (*cache.AdminStatsCache).
type DashboardCache interface {
	Snapshot(ctx context.Context, load func() (dto.DashboardSnapshot, error)) (dto.DashboardSnapshot, error)
}

// AdminStatsService собирает дашборд админки. Только чтение, без журнала.
type AdminStatsService struct {
	repo  StatsRepository
	cache DashboardCache
	loc   *time.Location
	// now — часы; тесты подменяют их, чтобы проверить границы суток.
	now func() time.Time
}

// NewAdminStatsService — loc задаёт пояс, в котором режутся сутки графиков
// (cfg.AdminTimezone). nil — UTC.
func NewAdminStatsService(r StatsRepository, c DashboardCache, loc *time.Location) *AdminStatsService {
	if loc == nil {
		loc = time.UTC
	}
	return &AdminStatsService{repo: r, cache: c, loc: loc, now: time.Now}
}

// Dashboard возвращает данные главной страницы.
//
// Снапшот (счётчики и ряды) берётся из кеша, жалобы и лента — всегда из БД.
// Любой сбой — ошибка целиком: показывать половину цифр рядом с плашкой
// «что-то не загрузилось» хуже, чем честное «статистика недоступна».
func (s *AdminStatsService) Dashboard(ctx context.Context) (dto.AdminDashboard, error) {
	snap, err := s.cache.Snapshot(ctx, func() (dto.DashboardSnapshot, error) {
		return s.buildSnapshot(ctx)
	})
	if err != nil {
		return dto.AdminDashboard{}, fmt.Errorf("dashboard snapshot: %w", err)
	}

	open, err := s.repo.OpenReports(ctx)
	if err != nil {
		return dto.AdminDashboard{}, fmt.Errorf("dashboard open reports: %w", err)
	}

	feed, err := s.repo.RecentAudit(ctx, recentActionsLimit, dashboardFeedExcluded)
	if err != nil {
		return dto.AdminDashboard{}, fmt.Errorf("dashboard recent actions: %w", err)
	}
	actions := make([]dto.AdminAuditRow, 0, len(feed))
	for _, f := range feed {
		actions = append(actions, dto.AdminAuditRow{
			ID:         f.ID,
			AdminEmail: f.AdminEmail,
			Action:     f.Action,
			TargetType: f.TargetType,
			TargetID:   f.TargetID,
			CreatedAt:  f.CreatedAt,
		})
	}

	return dto.AdminDashboard{
		Snapshot:           snap,
		OpenReports:        open.Count,
		OldestOpenReportAt: open.OldestAt,
		RecentActions:      actions,
	}, nil
}

// buildSnapshot считает тяжёлую часть. now приводится к UTC до похода в
// репозиторий: см. «Время в SQL — только UTC» в doc-комментарии StatsRepo.Totals.
func (s *AdminStatsService) buildSnapshot(ctx context.Context) (dto.DashboardSnapshot, error) {
	now := s.now().UTC()

	t, err := s.repo.Totals(ctx, now)
	if err != nil {
		return dto.DashboardSnapshot{}, fmt.Errorf("totals: %w", err)
	}

	snap := dto.DashboardSnapshot{
		GeneratedAt:             now,
		UsersTotal:              t.UsersTotal,
		UsersBanned:             t.UsersBanned,
		NewUsers7d:              t.NewUsers7d,
		Active1d:                t.Active1d,
		Active7d:                t.Active7d,
		Active30d:               t.Active30d,
		MeetupsUpcoming:         t.MeetupsUpcoming,
		MeetupsOfficialUpcoming: t.MeetupsOfficialUpcoming,
		NewMeetups7d:            t.NewMeetups7d,
		Messages1d:              t.Messages1d,
		Messages7d:              t.Messages7d,
	}

	since, days := dayWindow(now, s.loc, dashboardDays)
	for _, item := range []struct {
		series repo.StatsSeries
		dst    *dto.DailySeries
	}{
		{repo.StatsSeriesRegistrations, &snap.Registrations},
		{repo.StatsSeriesMeetups, &snap.Meetups},
		{repo.StatsSeriesMessages, &snap.Messages},
	} {
		counts, err := s.repo.DailyCounts(ctx, item.series, since, s.loc.String())
		if err != nil {
			return dto.DashboardSnapshot{}, fmt.Errorf("daily %s: %w", item.series, err)
		}
		*item.dst = fillSeries(days, counts)
	}
	return snap, nil
}

// dayWindow возвращает начало окна — полночь первого дня в поясе loc,
// переведённую в UTC, — и даты n дней ("2006-01-02") от старого к сегодняшнему.
// «Сегодня» — по loc: в 01:30 по Москве это уже новые сутки, хотя в UTC
// ещё вчера.
func dayWindow(now time.Time, loc *time.Location, n int) (time.Time, []string) {
	local := now.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	first := today.AddDate(0, 0, -(n - 1))

	days := make([]string, n)
	for i := range n {
		days[i] = first.AddDate(0, 0, i).Format(time.DateOnly)
	}
	return first.UTC(), days
}

// fillSeries раскладывает ответ репозитория по дням окна. День без событий
// получает ноль; день вне окна (не должен прийти, но SQL режет по UTC-границе)
// отбрасывается.
func fillSeries(days []string, counts []repo.DayCount) dto.DailySeries {
	byDay := make(map[string]int, len(counts))
	for _, c := range counts {
		byDay[c.Day] = c.Count
	}

	s := dto.DailySeries{Points: make([]dto.DayPoint, len(days))}
	for i, d := range days {
		n := byDay[d]
		s.Points[i] = dto.DayPoint{Date: d, Count: n}
		s.Total += n
		s.Max = max(s.Max, n)
	}
	return s
}
