package repo

import (
	"context"
	"fmt"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/uptrace/bun"
)

type AuditRepo struct {
	db *bun.DB
}

func NewAuditRepo(db *bun.DB) *AuditRepo {
	return &AuditRepo{db: db}
}

// Record пишет запись журнала. Принимает bun.IDB, а не только *bun.DB,
// сознательно: начиная с милстоуна B админские мутации будут оборачиваться в
// транзакцию, и запись аудита обязана попасть в ТУ ЖЕ транзакцию — иначе
// откат мутации оставит в журнале след действия, которого не было (или
// наоборот). Передавай nil, чтобы писать вне транзакции.
func (r *AuditRepo) Record(ctx context.Context, tx bun.IDB, e *domain.AuditEntry) error {
	db := tx
	if db == nil {
		db = r.db
	}
	if _, err := db.NewInsert().Model(e).Exec(ctx); err != nil {
		return fmt.Errorf("insert audit entry: %w", err)
	}
	return nil
}

// DB отдаёт соединение для случаев, когда вызывающему нужно открыть
// транзакцию, охватывающую и мутацию, и запись журнала.
func (r *AuditRepo) DB() bun.IDB {
	return r.db
}
