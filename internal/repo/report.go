package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/uptrace/bun"
)

var (
	ErrReportNotFound = errors.New("report not found")
	// ErrReportTargetNotFound — цель не существует ИЛИ жалующемуся не видна.
	// Два случая намеренно неотличимы: иначе по ответу на жалобу можно было бы
	// перебором выяснить, существует ли сообщение в чужом чате или приватный
	// митап с данным id.
	ErrReportTargetNotFound = errors.New("report target not found")
	ErrReportOwnTarget      = errors.New("report targets reporter's own content")
	ErrReportDuplicate      = errors.New("open report already exists")
	ErrReportClosed         = errors.New("report already closed")
)

// ReportQuery — критерии админского списка жалоб. Пустая строка — без фильтра.
type ReportQuery struct {
	Status     string
	TargetType string
	Limit      int
	Offset     int
}

type ReportRepo struct {
	db *bun.DB
}

func NewReportRepo(db *bun.DB) *ReportRepo {
	return &ReportRepo{db: db}
}

// RunInTx выполняет fn в транзакции. Нужен сервису модерации: действие над
// контентом, закрытие жалоб и запись журнала обязаны быть атомарны.
func (r *ReportRepo) RunInTx(ctx context.Context, fn func(tx bun.Tx) error) error {
	return r.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return fn(tx)
	})
}

// reportTarget — то, что жалоба узнаёт о цели в момент подачи.
type reportTarget struct {
	OwnerID int64   `bun:"owner_id"`
	Text    string  `bun:"text"`
	FileKey *string `bun:"file_key"`
}

// loadReportTarget находит цель, проверяет, что жалующемуся она ВИДНА, и
// снимает снимок контента. Всё — одним запросом на каждый тип.
//
// Правила видимости повторяют доступ в приложении:
//   - сообщение — жалующийся состоит в чате этого сообщения;
//   - митап — публичный, либо жалующийся его создатель или участник;
//   - пользователь — существует и не удалён.
//
// Любой отказ даёт ErrReportTargetNotFound (см. комментарий к сентинелу).
func loadReportTarget(ctx context.Context, idb bun.IDB, reporterID int64, typ domain.ReportTargetType, id int64) (*reportTarget, error) {
	var (
		query string
		args  []any
	)
	switch typ {
	case domain.ReportTargetMessage:
		query = `SELECT m.sender_id AS owner_id, m.content AS text, f.key AS file_key
			FROM messages m
			LEFT JOIN files f ON f.id = m.file_id
			WHERE m.id = ? AND m.deleted_at IS NULL
			  AND EXISTS (SELECT 1 FROM chat_participants cp
			              WHERE cp.chat_id = m.chat_id AND cp.user_id = ?)`
		args = []any{id, reporterID}
	case domain.ReportTargetMeetup:
		query = `SELECT mt.creator_id AS owner_id,
			       concat_ws(E'\n\n', mt.title, mt.description) AS text,
			       f.key AS file_key
			FROM meetups mt
			LEFT JOIN files f ON f.id = mt.cover_file_id
			WHERE mt.id = ? AND mt.deleted_at IS NULL
			  AND (mt.is_public OR mt.creator_id = ?
			       OR EXISTS (SELECT 1 FROM participants p
			                  WHERE p.meetup_id = mt.id AND p.user_id = ?))`
		args = []any{id, reporterID, reporterID}
	case domain.ReportTargetUser:
		query = `SELECT u.id AS owner_id,
			       concat_ws(E'\n', p.username, p.display_name, p.bio) AS text,
			       f.key AS file_key
			FROM users u
			LEFT JOIN profile p ON p.user_id = u.id
			LEFT JOIN files f ON f.id = p.avatar_file_id
			WHERE u.id = ? AND u.deleted_at IS NULL`
		args = []any{id}
	default:
		return nil, ErrReportTargetNotFound
	}

	var t reportTarget
	err := idb.NewRaw(query, args...).Scan(ctx, &t)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrReportTargetNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load report target: %w", err)
	}
	return &t, nil
}

// Create проверяет видимость цели, снимает снимок и вставляет жалобу —
// в одной транзакции, чтобы снимок соответствовал ровно тому, что видел
// жалующийся в момент проверки.
//
// Заполняет в rep: TargetOwnerID, SnapshotText, SnapshotFileKey, Status, ID,
// CreatedAt. Вызывающий задаёт ReporterID, TargetType, TargetID, Reason, Comment.
func (r *ReportRepo) Create(ctx context.Context, rep *domain.Report) error {
	return r.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		target, err := loadReportTarget(ctx, tx, rep.ReporterID, rep.TargetType, rep.TargetID)
		if err != nil {
			return err
		}
		if target.OwnerID == rep.ReporterID {
			return ErrReportOwnTarget
		}

		rep.TargetOwnerID = target.OwnerID
		rep.SnapshotText = target.Text
		rep.SnapshotFileKey = target.FileKey
		rep.Status = domain.ReportStatusOpen

		if _, err := tx.NewInsert().Model(rep).Returning("id, created_at").Exec(ctx); err != nil {
			if name, ok := uniqueViolation(err); ok && strings.Contains(name, "uq_reports_open_per_reporter") {
				return ErrReportDuplicate
			}
			return fmt.Errorf("insert report: %w", err)
		}
		return nil
	})
}

// List отдаёт страницу жалоб и общее число подходящих строк (ScanAndCount —
// см. UserAdminRepo.List о том, почему не два запроса).
//
// Открытые — от старых к новым: это очередь, и первой разбирается та, что
// ждёт дольше всех. Закрытые — от новых к старым: это история.
func (r *ReportRepo) List(ctx context.Context, q ReportQuery) ([]domain.Report, int, error) {
	var reports []domain.Report

	query := r.db.NewSelect().Model(&reports)
	if q.Status != "" {
		query = query.Where("status = ?", q.Status)
	}
	if q.TargetType != "" {
		query = query.Where("target_type = ?", q.TargetType)
	}
	if q.Status == string(domain.ReportStatusOpen) {
		query = query.OrderExpr("id ASC")
	} else {
		query = query.OrderExpr("id DESC")
	}
	if q.Limit > 0 {
		query = query.Limit(q.Limit)
	}
	if q.Offset > 0 {
		query = query.Offset(q.Offset)
	}

	total, err := query.ScanAndCount(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list reports: %w", err)
	}
	return reports, total, nil
}

// GetByID отдаёт жалобу. Отсутствие — ErrReportNotFound.
func (r *ReportRepo) GetByID(ctx context.Context, id int64) (*domain.Report, error) {
	rep := new(domain.Report)
	err := r.db.NewSelect().Model(rep).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrReportNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get report: %w", err)
	}
	return rep, nil
}

// CloseTx закрывает ОДНУ открытую жалобу (отклонение модератором).
//
// Ноль обновлённых строк различается повторным чтением: «нет такой» —
// ErrReportNotFound, «уже закрыта» — ErrReportClosed. Второй случай —
// штатный двойной клик, и модератор должен увидеть осмысленный ответ.
func (r *ReportRepo) CloseTx(ctx context.Context, tx bun.IDB, id, adminID int64, status domain.ReportStatus, resolution string) error {
	res, err := tx.NewUpdate().
		Model((*domain.Report)(nil)).
		Set("status = ?", status).
		Set("resolution = ?", resolution).
		Set("resolved_by = ?", adminID).
		Set("resolved_at = ?", time.Now().UTC()).
		Where("id = ?", id).
		Where("status = ?", domain.ReportStatusOpen).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("close report: %w", err)
	}

	rowsErr := expectOneRow(res, ErrReportClosed)
	if rowsErr == nil || !errors.Is(rowsErr, ErrReportClosed) {
		return rowsErr
	}

	exists, err := tx.NewSelect().Model((*domain.Report)(nil)).Where("id = ?", id).Exists(ctx)
	if err != nil {
		return fmt.Errorf("close report: %w", err)
	}
	if !exists {
		return ErrReportNotFound
	}
	return ErrReportClosed
}

// ResolveTargetTx закрывает ВСЕ открытые жалобы на цель как resolved.
// Возвращает их число — оно уходит в журнал: «одним действием закрыли 5 жалоб».
func (r *ReportRepo) ResolveTargetTx(ctx context.Context, tx bun.IDB, targetType domain.ReportTargetType, targetID, adminID int64, resolution string) (int, error) {
	res, err := tx.NewUpdate().
		Model((*domain.Report)(nil)).
		Set("status = ?", domain.ReportStatusResolved).
		Set("resolution = ?", resolution).
		Set("resolved_by = ?", adminID).
		Set("resolved_at = ?", time.Now().UTC()).
		Where("target_type = ?", targetType).
		Where("target_id = ?", targetID).
		Where("status = ?", domain.ReportStatusOpen).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("resolve reports: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return int(n), nil
}
