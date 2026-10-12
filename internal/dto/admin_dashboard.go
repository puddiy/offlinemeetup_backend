package dto

import "time"

// AdminDashboard — всё, что показывает главная страница админки. Snapshot
// кешируется (тяжёлые счётчики), остальные поля читаются на каждый показ:
// модератор, разобравший жалобу, должен сразу увидеть очередь без неё.
type AdminDashboard struct {
	Snapshot           DashboardSnapshot `json:"snapshot"`
	OpenReports        int               `json:"open_reports"`
	OldestOpenReportAt *time.Time        `json:"oldest_open_report_at"`
	RecentActions      []AdminAuditRow   `json:"recent_actions"`
}

// DashboardSnapshot — тяжёлая часть дашборда. Лежит в Redis JSON-ом под
// cache.AdminStatsKey: при переименовании или смене типа поля подними версию
// в ключе, иначе старый снапшот молча раскодируется с нулями до истечения TTL.
type DashboardSnapshot struct {
	GeneratedAt time.Time `json:"generated_at"`

	UsersTotal  int `json:"users_total"`
	UsersBanned int `json:"users_banned"`
	NewUsers7d  int `json:"new_users_7d"`

	Active1d  int `json:"active_1d"`
	Active7d  int `json:"active_7d"`
	Active30d int `json:"active_30d"`

	MeetupsUpcoming         int `json:"meetups_upcoming"`
	MeetupsOfficialUpcoming int `json:"meetups_official_upcoming"`
	NewMeetups7d            int `json:"new_meetups_7d"`

	Messages1d int `json:"messages_1d"`
	Messages7d int `json:"messages_7d"`

	Registrations DailySeries `json:"registrations"`
	Meetups       DailySeries `json:"meetups"`
	Messages      DailySeries `json:"messages"`
}

// DailySeries — ряд по дням, от старого дня к сегодняшнему, без пропусков:
// день без событий — точка с нулём.
type DailySeries struct {
	Points []DayPoint `json:"points"`
	Total  int        `json:"total"`
	Max    int        `json:"max"`
}

// DayPoint — один день ряда. Date — "2006-01-02" в поясе админки.
type DayPoint struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

// AdminAuditRow — строка ленты действий. AdminEmail пуст, если учётку админа
// с тех пор удалили (журнал её переживает).
type AdminAuditRow struct {
	ID         int64     `json:"id"`
	AdminEmail string    `json:"admin_email"`
	Action     string    `json:"action"`
	TargetType string    `json:"target_type"`
	TargetID   string    `json:"target_id"`
	CreatedAt  time.Time `json:"created_at"`
}
