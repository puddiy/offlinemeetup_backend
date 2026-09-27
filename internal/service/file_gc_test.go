package service

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo/mocks"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"go.uber.org/mock/gomock"
)

func orphan(key string) domain.File {
	return domain.File{ID: uuid.New(), Bucket: "media", Key: key}
}

func TestFileGCSweepDeletesObjectBeforeRow(t *testing.T) {
	store := mocks.NewMockOrphanFileStore(gomock.NewController(t))
	s3c := &fakeS3Deleter{}
	a, b := orphan("uploads/a.png"), orphan("uploads/b.png")

	store.EXPECT().ListOrphans(gomock.Any(), 24*time.Hour, 200).Return([]domain.File{a, b}, nil)
	for _, f := range []domain.File{a, b} {
		store.EXPECT().DeleteTx(gomock.Any(), gomock.Nil(), f.ID).
			DoAndReturn(func(_ context.Context, _ bun.IDB, id uuid.UUID) error {
				want := "media/" + map[uuid.UUID]string{a.ID: a.Key, b.ID: b.Key}[id]
				require.Contains(t, s3c.deleted, want, "строка удаляется только после объекта")
				return nil
			})
	}

	gc := NewFileGC(store, s3c, 24*time.Hour, 200, slog.New(slog.DiscardHandler))
	n, err := gc.Sweep(context.Background())

	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.Equal(t, []string{"media/uploads/a.png", "media/uploads/b.png"}, s3c.deleted)
}

// Сбой S3 на одном файле не останавливает уборку остальных, а его строка
// остаётся — по ней следующий проход попробует снова.
func TestFileGCSweepKeepsRowWhenS3Fails(t *testing.T) {
	store := mocks.NewMockOrphanFileStore(gomock.NewController(t))
	s3c := &fakeS3Deleter{err: errors.New("s3 down")}

	store.EXPECT().ListOrphans(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.File{orphan("k1"), orphan("k2")}, nil)
	// DeleteTx не ожидается: gomock провалит тест, если строку тронут.

	gc := NewFileGC(store, s3c, time.Hour, 10, slog.New(slog.DiscardHandler))
	n, err := gc.Sweep(context.Background())

	require.NoError(t, err)
	require.Zero(t, n)
}

// Два инстанса могут убирать одну пачку одновременно: строку, которую уже
// удалил сосед, считаем убранной, а не ошибкой.
func TestFileGCSweepRowAlreadyGoneIsDone(t *testing.T) {
	store := mocks.NewMockOrphanFileStore(gomock.NewController(t))
	f := orphan("k")

	store.EXPECT().ListOrphans(gomock.Any(), gomock.Any(), gomock.Any()).Return([]domain.File{f}, nil)
	store.EXPECT().DeleteTx(gomock.Any(), gomock.Nil(), f.ID).Return(repo.ErrFileNotFound)

	gc := NewFileGC(store, &fakeS3Deleter{}, time.Hour, 10, slog.New(slog.DiscardHandler))
	n, err := gc.Sweep(context.Background())

	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestFileGCSweepPropagatesListError(t *testing.T) {
	store := mocks.NewMockOrphanFileStore(gomock.NewController(t))
	boom := errors.New("db down")
	store.EXPECT().ListOrphans(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, boom)

	gc := NewFileGC(store, &fakeS3Deleter{}, time.Hour, 10, slog.New(slog.DiscardHandler))
	_, err := gc.Sweep(context.Background())

	require.ErrorIs(t, err, boom)
}

// Паника в одном проходе не должна убивать цикл: иначе уборка молча
// остановится до перезапуска процесса. Второй проход обязан состояться.
func TestFileGCRunSurvivesPanicAndStopsOnCancel(t *testing.T) {
	store := mocks.NewMockOrphanFileStore(gomock.NewController(t))
	ctx, cancel := context.WithCancel(context.Background())

	var calls atomic.Int32
	store.EXPECT().ListOrphans(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, time.Duration, int) ([]domain.File, error) {
			if calls.Add(1) == 1 {
				panic("boom")
			}
			cancel()
			return nil, nil
		}).MinTimes(2)

	gc := NewFileGC(store, &fakeS3Deleter{}, time.Hour, 10, slog.New(slog.DiscardHandler))

	done := make(chan struct{})
	go func() {
		gc.Run(ctx, 10*time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run не остановился после отмены контекста")
	}
	require.GreaterOrEqual(t, calls.Load(), int32(2))
}
