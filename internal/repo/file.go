package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/uptrace/bun"
)

// ErrFileNotOwned means a user referenced a file uploaded by someone else (or a
// non-existent / owner-less file). Reference paths translate it to ErrForbidden.
var ErrFileNotOwned = errors.New("file is not owned by user")

// ErrFileNotImage means an owned file was referenced where only an image is
// allowed (meetup cover, profile avatar). Reference paths translate it to
// ErrInvalidInput.
var ErrFileNotImage = errors.New("file is not an image")

// ErrFileNotFound — строки файла нет.
var ErrFileNotFound = errors.New("file not found")

type FileRepo struct {
	db *bun.DB
}

func NewFileRepo(db *bun.DB) *FileRepo {
	return &FileRepo{db: db}
}

func (r *FileRepo) Create(ctx context.Context, file *domain.File) error {
	_, err := r.db.NewInsert().Model(file).Exec(ctx)
	return err
}

// GetByID отдаёт метаданные файла: ключ и бакет нужны, чтобы удалить объект
// из S3 раньше строки.
func (r *FileRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.File, error) {
	f := new(domain.File)
	err := r.db.NewSelect().Model(f).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrFileNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get file: %w", err)
	}
	return f, nil
}

// DeleteTx удаляет строку файла. Все ссылки на files объявлены
// ON DELETE SET NULL (profile.avatar_file_id, meetups.cover_file_id,
// messages.file_id), поэтому удаление строки само отвязывает файл отовсюду.
//
// Вызывать ТОЛЬКО после удаления объекта из S3: строка — единственный способ
// найти объект, и без неё он навсегда останется доступным по публичной ссылке.
//
// tx == nil — работа вне транзакции (тот же приём, что у AuditRepo.Record).
func (r *FileRepo) DeleteTx(ctx context.Context, tx bun.IDB, id uuid.UUID) error {
	db := tx
	if db == nil {
		db = r.db
	}
	res, err := db.NewDelete().Model((*domain.File)(nil)).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete file: %w", err)
	}
	return expectOneRow(res, ErrFileNotFound)
}

// fileOwnedBy reports whether the file exists and was uploaded by userID. Used
// to stop a user attaching another user's uploaded file (cover, avatar, message
// attachment) by replaying its id. Runs on the given IDB so it can join an
// existing transaction.
func fileOwnedBy(ctx context.Context, idb bun.IDB, fileID uuid.UUID, userID int64) (bool, error) {
	exists, err := idb.NewSelect().
		Model((*domain.File)(nil)).
		Where("id = ? AND uploaded_by = ?", fileID, userID).
		Exists(ctx)
	if err != nil {
		return false, fmt.Errorf("checking file ownership: %w", err)
	}
	return exists, nil
}

// imageFileOwnedBy checks that the file exists, was uploaded by userID, and is an
// image. Used by cover/avatar reference paths, which — unlike chat attachments —
// accept images only. Returns ErrFileNotOwned (missing/not owned) or
// ErrFileNotImage (owned but not image/*).
func imageFileOwnedBy(ctx context.Context, idb bun.IDB, fileID uuid.UUID, userID int64) error {
	var mimeType string
	err := idb.NewSelect().
		Model((*domain.File)(nil)).
		Column("mime_type").
		Where("id = ? AND uploaded_by = ?", fileID, userID).
		Scan(ctx, &mimeType)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrFileNotOwned
	}
	if err != nil {
		return fmt.Errorf("checking file ownership: %w", err)
	}
	if !strings.HasPrefix(mimeType, "image/") {
		return ErrFileNotImage
	}
	return nil
}
