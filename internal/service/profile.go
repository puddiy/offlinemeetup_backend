package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo"
)

type ProfileRepository interface {
	GetByUserID(ctx context.Context, userID int64) (*domain.Profile, error)
	UpdateProfile(ctx context.Context, profile *domain.Profile) (*domain.Profile, error)
}

type UserTagUpdater interface {
	UpdateTags(ctx context.Context, userID int64, tagIDs []int64) error
	GetTagsByUserID(ctx context.Context, userID int64) ([]domain.Tag, error)
}

// profileCache кеширует профили по userID и сбрасывает их при обновлении.
// Объявлен у потребителя, удовлетворяется *cache.ProfileCache.
type profileCache interface {
	Profile(ctx context.Context, userID int64, load func() (*dto.ProfileResponse, error)) (*dto.ProfileResponse, error)
	InvalidateProfile(ctx context.Context, userID int64) error
}

type ProfileService struct {
	profileRepo ProfileRepository
	userRepo    UserTagUpdater
	cache       profileCache
	s3PublicURL string
}

func NewProfileService(profileRepo ProfileRepository, userRepo UserTagUpdater, cache profileCache, s3PublicURL string) *ProfileService {
	return &ProfileService{profileRepo: profileRepo, userRepo: userRepo, cache: cache, s3PublicURL: s3PublicURL}
}

func mapTagsToDTO(tags []domain.Tag) []dto.TagResponse {
	if tags == nil {
		return []dto.TagResponse{}
	}
	dtos := make([]dto.TagResponse, len(tags))
	for i, t := range tags {
		dtos[i] = dto.TagResponse{
			ID:   t.ID,
			Name: t.Name,
		}
	}
	return dtos
}

func (s *ProfileService) GetProfile(ctx context.Context, userID int64) (*dto.ProfileResponse, error) {
	return s.cache.Profile(ctx, userID, func() (*dto.ProfileResponse, error) {
		profile, err := s.profileRepo.GetByUserID(ctx, userID)
		if err != nil {
			return nil, err
		}
		if profile == nil {
			return nil, fmt.Errorf("profile not found: %w", ErrNotFound)
		}

		tags, err := s.userRepo.GetTagsByUserID(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch tags: %w", err)
		}

		avatarURL := ""
		if profile.AvatarFile != nil {
			avatarURL = fmt.Sprintf("%s/%s", s.s3PublicURL, profile.AvatarFile.Key)
		}

		return &dto.ProfileResponse{
			ID:          profile.ID,
			UserID:      profile.UserID,
			Username:    profile.Username,
			DisplayName: profile.DisplayName,
			Bio:         profile.Bio,
			AvatarURL:   avatarURL,
			IsOrganizer: profile.IsOrganizer,
			Tags:        mapTagsToDTO(tags),
		}, nil
	})
}

func (s *ProfileService) UpdateProfile(ctx context.Context, userID int64, req dto.UpdateProfileRequest) (*dto.ProfileResponse, error) {
	existingProfile, err := s.profileRepo.GetByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get profile: %w", err)
	}

	if existingProfile == nil {
		existingProfile = &domain.Profile{UserID: userID}
	}

	if req.Username != nil {
		existingProfile.Username = *req.Username
	}

	if req.DisplayName != nil {
		existingProfile.DisplayName = req.DisplayName
	}

	if req.Bio != nil {
		existingProfile.Bio = *req.Bio
	}

	if req.AvatarFileID != nil {
		if *req.AvatarFileID == "" {
			existingProfile.AvatarFileID = uuid.NullUUID{}
		} else {
			id, err := uuid.Parse(*req.AvatarFileID)
			if err != nil {
				// Зеркалим cover_file_id в meetup-сервисе: кривой UUID — это 400, а
				// не тихий no-op, иначе клиент думает, что аватар выставлен.
				return nil, fmt.Errorf("invalid avatar_file_id: %w", ErrInvalidInput)
			}
			existingProfile.AvatarFileID = uuid.NullUUID{UUID: id, Valid: true}
		}
	}

	_, err = s.profileRepo.UpdateProfile(ctx, existingProfile)
	if err != nil {
		if errors.Is(err, repo.ErrFileNotOwned) {
			return nil, fmt.Errorf("avatar file: %w", ErrForbidden)
		}
		if errors.Is(err, repo.ErrFileNotImage) {
			return nil, fmt.Errorf("avatar file must be an image: %w", ErrInvalidInput)
		}
		return nil, fmt.Errorf("updating profile error: %w", err)
	}

	if req.TagIDs != nil {
		if err := s.userRepo.UpdateTags(ctx, userID, req.TagIDs); err != nil {
			if errors.Is(err, repo.ErrTagUnavailable) {
				return nil, fmt.Errorf("tags: %w", ErrInvalidInput)
			}
			return nil, fmt.Errorf("updating tags error: %w", err)
		}
	}

	// Сбрасываем кеш ДО финального чтения, чтобы GetProfile перечитал свежие данные.
	_ = s.cache.InvalidateProfile(ctx, userID) // best-effort; cache layer logs failures

	return s.GetProfile(ctx, userID)
}
