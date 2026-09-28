package repo

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/uptrace/bun"
)

// MeetupAdminQuery — критерии админского списка митапов. Плоская структура
// без json-тегов, как UserQuery.
type MeetupAdminQuery struct {
	// Search — подстрока названия или точный id.
	Search string
	// Status: "" — все, "active" — впереди или идёт, "past" — прошёл,
	// "cancelled" — отменён.
	Status string
	// CreatorID != 0 — только митапы этого создателя (официальные).
	CreatorID int64

	Limit  int
	Offset int
}

// AdminList отдаёт страницу митапов и общее число подходящих (ScanAndCount —
// см. UserAdminRepo.List). Удалённые (deleted_at) Bun отфильтрует сам: у
// domain.Meetup тег soft_delete. Отменённые остаются — они часть истории.
func (r *MeetupRepo) AdminList(ctx context.Context, q MeetupAdminQuery) ([]domain.Meetup, int, error) {
	var meetups []domain.Meetup
	now := time.Now()

	query := r.db.NewSelect().
		Model(&meetups).
		Relation("Creator").
		Relation("Creator.Profile").
		Relation("Tags")

	switch q.Status {
	case "active":
		query = query.Where("?TableAlias.status = 'active'").Where("?TableAlias.end_time > ?", now)
	case "past":
		query = query.Where("?TableAlias.status = 'active'").Where("?TableAlias.end_time <= ?", now)
	case "cancelled":
		query = query.Where("?TableAlias.status = 'cancelled'")
	}

	if q.CreatorID != 0 {
		query = query.Where("?TableAlias.creator_id = ?", q.CreatorID)
	}

	if search := strings.TrimSpace(q.Search); search != "" {
		pattern := "%" + search + "%"
		query = query.WhereGroup(" AND ", func(sq *bun.SelectQuery) *bun.SelectQuery {
			sq = sq.Where("?TableAlias.title ILIKE ?", pattern)
			if id, err := strconv.ParseInt(search, 10, 64); err == nil {
				sq = sq.WhereOr("?TableAlias.id = ?", id)
			}
			return sq
		})
	}

	query = query.OrderExpr("?TableAlias.id DESC")
	if q.Limit > 0 {
		query = query.Limit(q.Limit)
	}
	if q.Offset > 0 {
		query = query.Offset(q.Offset)
	}

	total, err := query.ScanAndCount(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("admin list meetups: %w", err)
	}
	return meetups, total, nil
}
