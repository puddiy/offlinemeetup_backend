package domain

import (
	"time"

	"github.com/uptrace/bun"
)

// ReportTargetType — на что жалуются. Набор продублирован в CHECK-ограничении
// reports_target_type_check (миграция 20260926120000): меняешь здесь — меняй
// и там, иначе вставка упадёт на уровне БД.
type ReportTargetType string

const (
	ReportTargetMeetup  ReportTargetType = "meetup"
	ReportTargetMessage ReportTargetType = "message"
	ReportTargetUser    ReportTargetType = "user"
)

// Valid сообщает, что тип цели — один из известных.
func (t ReportTargetType) Valid() bool {
	switch t {
	case ReportTargetMeetup, ReportTargetMessage, ReportTargetUser:
		return true
	}
	return false
}

// ReportReason — причина жалобы. Набор продублирован в CHECK-ограничении
// reports_reason_check.
type ReportReason string

const (
	ReportReasonSpam       ReportReason = "spam"
	ReportReasonHarassment ReportReason = "harassment"
	ReportReasonHate       ReportReason = "hate"
	ReportReasonSexual     ReportReason = "sexual"
	ReportReasonViolence   ReportReason = "violence"
	ReportReasonScam       ReportReason = "scam"
	ReportReasonOther      ReportReason = "other"
)

// ReportReasons — причины в порядке показа пользователю. «Другое» последним:
// это запасной пункт, а не первый вариант, в который ткнут не глядя.
var ReportReasons = []ReportReason{
	ReportReasonSpam,
	ReportReasonHarassment,
	ReportReasonHate,
	ReportReasonSexual,
	ReportReasonViolence,
	ReportReasonScam,
	ReportReasonOther,
}

var reportReasonTitles = map[ReportReason]string{
	ReportReasonSpam:       "Спам или реклама",
	ReportReasonHarassment: "Оскорбления или травля",
	ReportReasonHate:       "Разжигание ненависти",
	ReportReasonSexual:     "Сексуальный контент",
	ReportReasonViolence:   "Насилие или угрозы",
	ReportReasonScam:       "Мошенничество",
	ReportReasonOther:      "Другое",
}

// Valid сообщает, что причина — одна из известных.
func (r ReportReason) Valid() bool {
	_, ok := reportReasonTitles[r]
	return ok
}

// Title — подпись причины для людей: клиенту и модератору. Для неизвестной
// причины — пустая строка.
func (r ReportReason) Title() string {
	return reportReasonTitles[r]
}

// ReportStatus — состояние жалобы. open — ждёт модератора; resolved — по цели
// приняты меры; dismissed — модератор счёл жалобу необоснованной.
type ReportStatus string

const (
	ReportStatusOpen      ReportStatus = "open"
	ReportStatusResolved  ReportStatus = "resolved"
	ReportStatusDismissed ReportStatus = "dismissed"
)

// Report — жалоба пользователя.
//
// TargetOwnerID — автор контента (отправитель сообщения, создатель митапа,
// сам пользователь). Хранится, чтобы модератор одним кликом попадал в карточку
// того, кого, возможно, надо забанить.
//
// SnapshotText / SnapshotFileKey — снимок контента НА МОМЕНТ ЖАЛОБЫ. Автор
// может отредактировать или удалить сообщение раньше, чем до жалобы дойдёт
// модератор; без снимка разбирать было бы нечего.
type Report struct {
	bun.BaseModel `bun:"table:reports"`

	ID              int64            `bun:",pk,autoincrement"`
	ReporterID      int64            `bun:",notnull"`
	TargetType      ReportTargetType `bun:",notnull"`
	TargetID        int64            `bun:",notnull"`
	TargetOwnerID   int64            `bun:",notnull"`
	Reason          ReportReason     `bun:",notnull"`
	Comment         string           `bun:",notnull"`
	SnapshotText    string           `bun:",notnull"`
	SnapshotFileKey *string          `bun:""`
	Status          ReportStatus     `bun:",notnull"`
	Resolution      *string          `bun:""`
	ResolvedBy      *int64           `bun:""`
	ResolvedAt      *time.Time       `bun:""`
	CreatedAt       time.Time        `bun:",nullzero,notnull,default:current_timestamp"`
}
