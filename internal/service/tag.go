package service

import (
	"context"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
)

type TagRepository interface {
	// ListVisible — теги каталога: скрытые (tags.is_hidden) в выбор не попадают.
	ListVisible(ctx context.Context) ([]domain.Tag, error)
}

// tagCache кеширует глобальный список тегов. Объявлен у потребителя,
// удовлетворяется *cache.TagCache.
type tagCache interface {
	ListTags(ctx context.Context, load func() ([]dto.TagResponse, error)) ([]dto.TagResponse, error)
}

type TagService struct {
	repo  TagRepository
	cache tagCache
}

func NewTagService(repo TagRepository, cache tagCache) *TagService {
	return &TagService{repo: repo, cache: cache}
}

func (s *TagService) ListTags(ctx context.Context) ([]dto.TagResponse, error) {
	return s.cache.ListTags(ctx, func() ([]dto.TagResponse, error) {
		tags, err := s.repo.ListVisible(ctx)
		if err != nil {
			return nil, err
		}

		res := make([]dto.TagResponse, len(tags))
		for i, t := range tags {
			res[i] = dto.TagResponse{
				ID:   t.ID,
				Name: t.Name,
			}
		}
		return res, nil
	})
}
