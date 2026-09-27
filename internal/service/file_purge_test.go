package service

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo/mocks"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"go.uber.org/mock/gomock"
)

// fakeS3Deleter записывает удалённые объекты как "bucket/key". Нужен вместо
// мока, чтобы в тестах можно было спросить «удалён ли объект К ЭТОМУ МОМЕНТУ»
// изнутри ожидания удаления строки.
type fakeS3Deleter struct {
	deleted []string
	err     error
}

func (f *fakeS3Deleter) DeleteObject(_ context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.deleted = append(f.deleted, aws.ToString(in.Bucket)+"/"+aws.ToString(in.Key))
	return &s3.DeleteObjectOutput{}, nil
}

// Главный инвариант: строка удаляется ТОЛЬКО после объекта. Строка — это
// единственный способ найти объект; удали её первой при сбое S3, и файл
// навсегда останется доступным по публичной ссылке.
func TestPurgeFileDeletesObjectBeforeRow(t *testing.T) {
	files := mocks.NewMockFileStore(gomock.NewController(t))
	s3c := &fakeS3Deleter{}
	id := uuid.New()

	files.EXPECT().GetByID(gomock.Any(), id).
		Return(&domain.File{ID: id, Bucket: "media", Key: "uploads/a.png"}, nil)
	files.EXPECT().DeleteTx(gomock.Any(), gomock.Nil(), id).
		DoAndReturn(func(context.Context, bun.IDB, uuid.UUID) error {
			require.Equal(t, []string{"media/uploads/a.png"}, s3c.deleted,
				"к моменту удаления строки объект обязан быть уже удалён")
			return nil
		})

	require.NoError(t, purgeFile(context.Background(), files, s3c, id))
}

// Сбой S3 — строку не трогаем: по ней потом можно найти и дочистить объект.
func TestPurgeFileKeepsRowWhenS3Fails(t *testing.T) {
	files := mocks.NewMockFileStore(gomock.NewController(t))
	s3c := &fakeS3Deleter{err: errors.New("s3 down")}
	id := uuid.New()

	files.EXPECT().GetByID(gomock.Any(), id).Return(&domain.File{ID: id, Bucket: "media", Key: "k"}, nil)
	// DeleteTx не ожидается: gomock провалит тест, если его вызовут.

	require.Error(t, purgeFile(context.Background(), files, s3c, id))
}

// Файла уже нет — задача выполнена, это не ошибка (повторный вызов).
func TestPurgeFileMissingRowIsNoop(t *testing.T) {
	files := mocks.NewMockFileStore(gomock.NewController(t))
	s3c := &fakeS3Deleter{}
	id := uuid.New()

	files.EXPECT().GetByID(gomock.Any(), id).Return(nil, repo.ErrFileNotFound)

	require.NoError(t, purgeFile(context.Background(), files, s3c, id))
	require.Empty(t, s3c.deleted)
}
