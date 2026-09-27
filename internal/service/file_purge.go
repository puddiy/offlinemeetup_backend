package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo"
	"github.com/uptrace/bun"
)

// S3DeleteObjectAPI — удаление объекта из S3. Удовлетворяется *s3.Client.
type S3DeleteObjectAPI interface {
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

// FileStore — то, что нужно для удаления файла. Удовлетворяется *repo.FileRepo.
type FileStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.File, error)
	GetByKey(ctx context.Context, key string) (*domain.File, error)
	DeleteTx(ctx context.Context, tx bun.IDB, id uuid.UUID) error
}

// deleteStoredObject удаляет объект файла из S3. Удаление отсутствующего ключа
// S3 считает успехом, поэтому повтор после частичного сбоя безопасен.
func deleteStoredObject(ctx context.Context, s3c S3DeleteObjectAPI, f *domain.File) error {
	_, err := s3c.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(f.Bucket),
		Key:    aws.String(f.Key),
	})
	if err != nil {
		return fmt.Errorf("delete s3 object %s: %w", f.Key, err)
	}
	return nil
}

// purgeFile удаляет файл целиком: сначала объект в S3, потом строку files.
//
// Порядок обязателен. Бакет публичный: пока объект лежит в S3, он доступен
// по прямой ссылке. Удали строку первой, упади на S3 — и объект навсегда
// останется в сети без записи, по которой его можно найти. В обратном порядке
// сбой оставляет строку, и повтор доводит дело до конца.
//
// Ссылки на файл (аватар, обложка, вложение) отвязываются сами: все внешние
// ключи на files объявлены ON DELETE SET NULL.
func purgeFile(ctx context.Context, files FileStore, s3c S3DeleteObjectAPI, id uuid.UUID) error {
	f, err := files.GetByID(ctx, id)
	if errors.Is(err, repo.ErrFileNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("purge file: %w", err)
	}

	if err := deleteStoredObject(ctx, s3c, f); err != nil {
		return err
	}

	if err := files.DeleteTx(ctx, nil, id); err != nil && !errors.Is(err, repo.ErrFileNotFound) {
		return fmt.Errorf("purge file row: %w", err)
	}
	return nil
}
