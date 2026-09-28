package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/cache"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service/mocks"
)

// newTagService собирает TagService со свежим кэшем (miniredis) на каждый тест,
// чтобы кэш не протекал между подтестами.
func newTagService(t *testing.T) (*mocks.MockTagRepository, *TagService) {
	t.Helper()
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockTagRepository(ctrl)

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	tc := cache.NewTagCache(cache.NewRedisCache(rdb, slog.New(slog.DiscardHandler)), cache.NopMetrics, time.Minute)

	return repo, NewTagService(repo, tc)
}

func TestTagService_ListTags(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		repo, srv := newTagService(t)
		tags := []domain.Tag{
			{ID: 1, Name: "Tech"},
			{ID: 2, Name: "Sport"},
		}
		repo.EXPECT().ListVisible(ctx).Return(tags, nil)

		resp, err := srv.ListTags(ctx)
		assert.NoError(t, err)
		assert.Len(t, resp, 2)
		assert.Equal(t, int64(1), resp[0].ID)
		assert.Equal(t, "Tech", resp[0].Name)
		assert.Equal(t, int64(2), resp[1].ID)
		assert.Equal(t, "Sport", resp[1].Name)
	})

	t.Run("repo_error", func(t *testing.T) {
		repo, srv := newTagService(t)
		repoErr := errors.New("db error")
		repo.EXPECT().ListVisible(ctx).Return(nil, repoErr)

		resp, err := srv.ListTags(ctx)
		assert.ErrorIs(t, err, repoErr)
		assert.Nil(t, resp)
	})

	t.Run("empty_list", func(t *testing.T) {
		repo, srv := newTagService(t)
		repo.EXPECT().ListVisible(ctx).Return([]domain.Tag{}, nil)

		resp, err := srv.ListTags(ctx)
		assert.NoError(t, err)
		assert.Len(t, resp, 0)
	})

	t.Run("second read is served from cache", func(t *testing.T) {
		repo, srv := newTagService(t)
		tags := []domain.Tag{{ID: 1, Name: "Tech"}}
		// GetAll должен вызваться РОВНО один раз на два чтения.
		repo.EXPECT().ListVisible(ctx).Return(tags, nil).Times(1)

		first, err := srv.ListTags(ctx)
		assert.NoError(t, err)
		assert.Len(t, first, 1)

		second, err := srv.ListTags(ctx)
		assert.NoError(t, err)
		assert.Equal(t, first, second)
	})
}
