package dto

import "time"

// AdminMeetupRow — строка админского списка митапов.
//
// Status — ВЫЧИСЛЕННЫЙ статус для человека: "active" (впереди или идёт),
// "past" (прошёл) или "cancelled". В БД «прошедший» — это status='active'
// с end_time в прошлом, и показывать его как active значило бы путать
// модератора.
type AdminMeetupRow struct {
	ID                int64
	Title             string
	Status            string
	IsPublic          bool
	IsOfficial        bool
	StartTime         time.Time
	EndTime           time.Time
	CreatorID         int64
	CreatorName       string
	ParticipantsCount int
	Tags              []string
}

// AdminMeetupDetail — карточка митапа в админке.
type AdminMeetupDetail struct {
	AdminMeetupRow
	Description string
	Address     string
	Lat         float64
	Lng         float64
	CoverURL    string
	TagIDs      []int64
	// InviteToken заполнен ТОЛЬКО у официальных митапов: их создатель —
	// служебный аккаунт, от имени которого действует админ. У чужих
	// митапов инвайт-токен остаётся creator-only (docs/SECURITY-INVARIANTS.md).
	InviteToken string
}
