package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"
)

// StatsRepo — агрегаты для дашборда админки. Только чтение. Запросы идут
// полным проходом по таблицам без своих индексов: сервис кеширует результат
// на несколько минут, и на текущих объёмах это дешевле поддержки индексов
// (см. «Вне скоупа» в плане Milestone E).
type StatsRepo struct {
	db *bun.DB
}

func NewStatsRepo(db *bun.DB) *StatsRepo {
	return &StatsRepo{db: db}
}

// StatsTotals — счётчики плиток дашборда.
//
// «Активен» — есть строка refresh_tokens, созданная в окне: refresh-токен
// ротируется на каждом обновлении access-токена (15 минут), так что любой
// заход в приложение оставляет строку. Других следов визита в БД нет.
type StatsTotals struct {
	UsersTotal              int `bun:"users_total"`
	UsersBanned             int `bun:"users_banned"`
	NewUsers7d              int `bun:"new_users_7d"`
	Active1d                int `bun:"active_1d"`
	Active7d                int `bun:"active_7d"`
	Active30d               int `bun:"active_30d"`
	MeetupsUpcoming         int `bun:"meetups_upcoming"`
	MeetupsOfficialUpcoming int `bun:"meetups_official_upcoming"`
	NewMeetups7d            int `bun:"new_meetups_7d"`
	Messages1d              int `bun:"messages_1d"`
	Messages7d              int `bun:"messages_7d"`
}

// Totals считает все счётчики одним запросом. Окна скользящие от now.
//
// now приводится к UTC ЗДЕСЬ, а не только у вызывающего: TIMESTAMP-колонки
// (users.created_at, meetups.*) хранят UTC без пояса, а bun пишет время
// литералом со смещением, которое приведение к TIMESTAMP молча отбрасывает.
// Не-UTC now сдвинул бы окна на смещение пояса.
//
// Регистрации и новые митапы считаются событиями: аккаунт, удалённый позже,
// и отменённый митап входят в «за 7 дней». UsersTotal — состояние: только
// живые аккаунты.
func (r *StatsRepo) Totals(ctx context.Context, now time.Time) (StatsTotals, error) {
	now = now.UTC()
	day := now.Add(-24 * time.Hour)
	week := now.AddDate(0, 0, -7)
	month := now.AddDate(0, 0, -30)

	var t StatsTotals
	err := r.db.NewRaw(`
SELECT
  (SELECT count(*) FROM users WHERE deleted_at IS NULL AND NOT is_system) AS users_total,
  (SELECT count(*) FROM users WHERE deleted_at IS NULL AND NOT is_system AND status = 'banned') AS users_banned,
  (SELECT count(*) FROM users WHERE NOT is_system AND created_at >= ?) AS new_users_7d,
  (SELECT count(DISTINCT user_id) FROM refresh_tokens WHERE created_at >= ?) AS active_1d,
  (SELECT count(DISTINCT user_id) FROM refresh_tokens WHERE created_at >= ?) AS active_7d,
  (SELECT count(DISTINCT user_id) FROM refresh_tokens WHERE created_at >= ?) AS active_30d,
  (SELECT count(*) FROM meetups WHERE status = 'active' AND end_time > ?) AS meetups_upcoming,
  (SELECT count(*) FROM meetups m JOIN users u ON u.id = m.creator_id
     WHERE u.is_system AND m.status = 'active' AND m.end_time > ?) AS meetups_official_upcoming,
  (SELECT count(*) FROM meetups WHERE created_at >= ?) AS new_meetups_7d,
  (SELECT count(*) FROM messages WHERE created_at >= ?) AS messages_1d,
  (SELECT count(*) FROM messages WHERE created_at >= ?) AS messages_7d`,
		week, day, week, month, now, now, week, day, week,
	).Scan(ctx, &t)
	if err != nil {
		return StatsTotals{}, fmt.Errorf("select stats totals: %w", err)
	}
	return t, nil
}

// StatsSeries — дневной ряд дашборда.
type StatsSeries string

const (
	StatsSeriesRegistrations StatsSeries = "registrations"
	StatsSeriesMeetups       StatsSeries = "meetups"
	StatsSeriesMessages      StatsSeries = "messages"
)

// ErrUnknownStatsSeries — ряда нет в statsSeriesSQL.
var ErrUnknownStatsSeries = errors.New("unknown stats series")

// statsSeriesSQL — белый список запросов рядов. Текст запроса никогда не
// собирается из аргумента: имя ряда только выбирает строку отсюда.
//
// Первый плейсхолдер — пояс (имя IANA), второй — начало окна в UTC. День
// отдаётся строкой 'YYYY-MM-DD' в поясе админки: строка — ключ, по которому
// сервис раскладывает дни, и у неё нет пояса, который можно потерять при
// сканировании.
//
// users и meetups хранят TIMESTAMP в UTC без пояса, поэтому сначала
// `AT TIME ZONE 'UTC'` (→ timestamptz), затем в пояс админки. messages —
// уже timestamptz, ему хватает одного перевода.
var statsSeriesSQL = map[StatsSeries]string{
	StatsSeriesRegistrations: `
SELECT to_char((created_at AT TIME ZONE 'UTC') AT TIME ZONE ?, 'YYYY-MM-DD') AS day, count(*) AS count
FROM users
WHERE NOT is_system AND created_at >= ?
GROUP BY 1`,
	StatsSeriesMeetups: `
SELECT to_char((created_at AT TIME ZONE 'UTC') AT TIME ZONE ?, 'YYYY-MM-DD') AS day, count(*) AS count
FROM meetups
WHERE created_at >= ?
GROUP BY 1`,
	StatsSeriesMessages: `
SELECT to_char(created_at AT TIME ZONE ?, 'YYYY-MM-DD') AS day, count(*) AS count
FROM messages
WHERE created_at >= ?
GROUP BY 1`,
}

// DayCount — число событий за один день ряда. Дни без событий в ответ не
// попадают — нули дописывает сервис.
type DayCount struct {
	Day   string `bun:"day"`
	Count int    `bun:"count"`
}

// DailyCounts возвращает события ряда по дням начиная с since. tz — имя
// пояса IANA (time.Location.String()), в котором режутся сутки.
func (r *StatsRepo) DailyCounts(ctx context.Context, series StatsSeries, since time.Time, tz string) ([]DayCount, error) {
	query, ok := statsSeriesSQL[series]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownStatsSeries, series)
	}

	var rows []DayCount
	if err := r.db.NewRaw(query, tz, since.UTC()).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("select daily %s: %w", series, err)
	}
	return rows, nil
}

// OpenReportsSummary — очередь модерации одним взглядом.
type OpenReportsSummary struct {
	Count    int        `bun:"count"`
	OldestAt *time.Time `bun:"oldest_at"`
}

// OpenReports — число открытых жалоб и время самой старой. Идёт по
// idx_reports_status_created, поэтому его можно звать на каждый показ.
func (r *StatsRepo) OpenReports(ctx context.Context) (OpenReportsSummary, error) {
	var s OpenReportsSummary
	err := r.db.NewRaw(`
SELECT count(*) AS count, min(created_at) AS oldest_at
FROM reports
WHERE status = 'open'`).Scan(ctx, &s)
	if err != nil {
		return OpenReportsSummary{}, fmt.Errorf("select open reports summary: %w", err)
	}
	return s, nil
}

// AuditFeedRow — строка ленты последних действий.
type AuditFeedRow struct {
	ID         int64     `bun:"id"`
	AdminEmail string    `bun:"admin_email"`
	Action     string    `bun:"action"`
	TargetType string    `bun:"target_type"`
	TargetID   string    `bun:"target_id"`
	CreatedAt  time.Time `bun:"created_at"`
}

// RecentAudit — последние записи журнала, новые сверху.
//
// LEFT JOIN, а не JOIN: у admin_audit_log.admin_id нет внешнего ключа
// (журнал переживает удаление учётки, см. миграцию 20260916120000), и запись
// удалённого админа обязана остаться в ленте — с пустым email.
func (r *StatsRepo) RecentAudit(ctx context.Context, limit int, excludeActions []string) ([]AuditFeedRow, error) {
	q := r.db.NewSelect().
		TableExpr("admin_audit_log AS l").
		Join("LEFT JOIN admin_users AS a ON a.id = l.admin_id").
		ColumnExpr("l.id, coalesce(a.email, '') AS admin_email, l.action").
		ColumnExpr("coalesce(l.target_type, '') AS target_type, coalesce(l.target_id, '') AS target_id").
		ColumnExpr("l.created_at").
		OrderExpr("l.created_at DESC, l.id DESC").
		Limit(limit)
	if len(excludeActions) > 0 {
		q = q.Where("l.action NOT IN (?)", bun.In(excludeActions))
	}

	var rows []AuditFeedRow
	if err := q.Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("select recent audit: %w", err)
	}
	return rows, nil
}
