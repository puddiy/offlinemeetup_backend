package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/safego"
	"github.com/uptrace/bun"
)

// OrphanFileStore — то, что уборке файлов нужно от хранилища.
// Удовлетворяется *repo.FileRepo.
type OrphanFileStore interface {
	ListOrphans(ctx context.Context, minAge time.Duration, limit int) ([]domain.File, error)
	DeleteTx(ctx context.Context, tx bun.IDB, id uuid.UUID) error
}

// FileGC периодически удаляет файлы, на которые ничто не ссылается: загрузили
// и не прикрепили, или прикрепили к сообщению, которое потом удалили. Бакет
// публичный, поэтому без уборки такой файл навсегда остаётся доступным по
// прямой ссылке — в том числе фото из сообщения, которое автор удалил.
// Что именно считается сиротой — см. repo.FileRepo.ListOrphans.
type FileGC struct {
	files  OrphanFileStore
	s3     S3DeleteObjectAPI
	minAge time.Duration
	batch  int
	log    *slog.Logger
}

// NewFileGC: minAge — сколько файл обязан пролежать, прежде чем станет
// кандидатом (клиент сначала загружает файл и лишь потом отправляет
// сообщение или сохраняет профиль); batch — сколько файлов за один проход.
func NewFileGC(files OrphanFileStore, s3c S3DeleteObjectAPI, minAge time.Duration, batch int, log *slog.Logger) *FileGC {
	return &FileGC{files: files, s3: s3c, minAge: minAge, batch: batch, log: log}
}

// Sweep убирает одну пачку сирот и возвращает, сколько убрано.
//
// Порядок тот же, что у purgeFile: объект в S3, потом строка. Сбой S3 по
// одному файлу не прерывает проход — его строка остаётся, и следующий проход
// попробует снова. Строку, которую уже удалил параллельный инстанс, считаем
// убранной.
func (g *FileGC) Sweep(ctx context.Context) (int, error) {
	orphans, err := g.files.ListOrphans(ctx, g.minAge, g.batch)
	if err != nil {
		return 0, fmt.Errorf("list orphan files: %w", err)
	}

	removed := 0
	for i := range orphans {
		f := &orphans[i]
		if err := deleteStoredObject(ctx, g.s3, f); err != nil {
			g.log.Error("file gc: deleting object", slog.String("key", f.Key), slog.Any("error", err))
			continue
		}
		if err := g.files.DeleteTx(ctx, nil, f.ID); err != nil && !errors.Is(err, repo.ErrFileNotFound) {
			g.log.Error("file gc: deleting row", slog.String("file_id", f.ID.String()), slog.Any("error", err))
			continue
		}
		removed++
	}
	return removed, nil
}

// Run выполняет Sweep каждые interval, пока не отменён ctx.
//
// Каждый проход обёрнут в свой recover: паника в одном проходе не должна
// останавливать уборку до перезапуска процесса, а safego.Go вокруг всего Run
// восстановил бы панику уже ПОСЛЕ выхода из цикла.
func (g *FileGC) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.sweepOnce(ctx)
		}
	}
}

func (g *FileGC) sweepOnce(ctx context.Context) {
	defer safego.Recover(g.log, "file gc sweep")

	n, err := g.Sweep(ctx)
	if err != nil {
		g.log.Error("file gc sweep failed", slog.Any("error", err))
		return
	}
	if n > 0 {
		g.log.Info("file gc removed orphan files", slog.Int("count", n))
	}
}
