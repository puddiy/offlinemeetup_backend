package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/uptrace/bun"
)

// Действия журнала. Строковые константы, а не enum в БД: набор будет расти
// с каждым милстоуном, и ALTER TYPE на каждое новое действие — лишняя
// миграция ради нулевой выгоды.
const (
	AuditActionLogin  = "admin.login"
	AuditActionLogout = "admin.logout"

	AuditActionUserBan    = "user.ban"
	AuditActionUserUnban  = "user.unban"
	AuditActionUserLogout = "user.logout_all"
	AuditActionUserDelete = "user.delete"
)

// AuditEvent — то, что вызывающий хочет записать в журнал. Отдельный тип, а
// не domain.AuditEntry напрямую: сервис сам проставляет CreatedAt и валидирует
// обязательные поля, а вызывающему не нужно знать про bun.BaseModel.
type AuditEvent struct {
	AdminID    int64
	Action     string
	TargetType string
	TargetID   string
	IP         string
	Details    map[string]any
}

// auditWriter — то, что AuditService требует от репозитория.
type auditWriter interface {
	Record(ctx context.Context, tx bun.IDB, e *domain.AuditEntry) error
}

// AuditRecorder — узкий интерфейс для потребителей журнала (хендлеры
// админки). Объявлен здесь, чтобы транспорт зависел от него, а не от
// конкретного *AuditService.
type AuditRecorder interface {
	Record(ctx context.Context, tx bun.IDB, ev AuditEvent) error
}

type AuditService struct {
	repo auditWriter
	log  *slog.Logger
}

func NewAuditService(r auditWriter, log *slog.Logger) *AuditService {
	return &AuditService{repo: r, log: log}
}

// Record пишет одно событие. tx пробрасывается насквозь: если вызывающий
// уже открыл транзакцию вокруг мутации, запись журнала обязана лечь в неё же,
// чтобы «сделали» и «записали, что сделали» были атомарны.
func (s *AuditService) Record(ctx context.Context, tx bun.IDB, ev AuditEvent) error {
	if ev.AdminID == 0 || ev.Action == "" {
		return ErrInvalidInput
	}

	entry := &domain.AuditEntry{
		AdminID:    ev.AdminID,
		Action:     ev.Action,
		TargetType: ev.TargetType,
		TargetID:   ev.TargetID,
		IP:         ev.IP,
		Details:    ev.Details,
		CreatedAt:  time.Now().UTC(),
	}

	return s.repo.Record(ctx, tx, entry)
}
