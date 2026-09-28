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
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/cache"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo"
)

// newProfileCache собирает ProfileCache со свежим miniredis на каждый тест.
func newProfileCache(t *testing.T) (*miniredis.Miniredis, *cache.ProfileCache) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	pc := cache.NewProfileCache(cache.NewRedisCache(rdb, slog.New(slog.DiscardHandler)), cache.NopMetrics, time.Minute)
	return mr, pc
}

// --- 1. Создаем моки для интерфейсов ---

// MockProfileRepo имитирует интерфейс ProfileRepository
type MockProfileRepo struct {
	mock.Mock
}

func (m *MockProfileRepo) GetByUserID(ctx context.Context, userID int64) (*domain.Profile, error) {
	args := m.Called(ctx, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Profile), args.Error(1)
}

func (m *MockProfileRepo) UpdateProfile(ctx context.Context, profile *domain.Profile) (*domain.Profile, error) {
	args := m.Called(ctx, profile)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Profile), args.Error(1)
}

// MockUserTagUpdater имитирует интерфейс UserTagUpdater
type MockUserTagUpdater struct {
	mock.Mock
}

func (m *MockUserTagUpdater) UpdateTags(ctx context.Context, userID int64, tagIDs []int64) error {
	args := m.Called(ctx, userID, tagIDs)
	return args.Error(0)
}

func (m *MockUserTagUpdater) GetTagsByUserID(ctx context.Context, userID int64) ([]domain.Tag, error) {
	args := m.Called(ctx, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]domain.Tag), args.Error(1)
}

// --- 2. Пишем табличные тесты ---

func TestProfileService_GetProfile(t *testing.T) {
	// Подготавливаем тестовые данные (Arrange)
	testUserID := int64(1)

	// Таблица тестовых сценариев
	tests := []struct {
		name          string                                                   // Название теста
		mockSetup     func(repo *MockProfileRepo, tagRepo *MockUserTagUpdater) // Настройка моков
		expectedError error                                                    // Ожидаемая ошибка (если есть)
		checkResult   func(t *testing.T, profile *dto.ProfileResponse)         // Функция для проверки результата (если нет ошибки)
	}{
		{
			name: "Успешное получение профиля",
			mockSetup: func(repo *MockProfileRepo, tagRepo *MockUserTagUpdater) {
				// Указываем: когда вызовут GetByUserID с нашими параметрами, верни профиль и nil ошибку
				repo.On("GetByUserID", mock.Anything, testUserID).
					Return(&domain.Profile{ID: 100, UserID: testUserID, Username: "john_doe"}, nil)

				// Указываем: когда вызовут GetTagsByUserID, верни массив тегов
				tagRepo.On("GetTagsByUserID", mock.Anything, testUserID).
					Return([]domain.Tag{{ID: 1, Name: "Golang"}}, nil)
			},
			expectedError: nil,
			checkResult: func(t *testing.T, profile *dto.ProfileResponse) {
				assert.NotNil(t, profile)
				assert.Equal(t, "john_doe", profile.Username)
				assert.Len(t, profile.Tags, 1)
				assert.Equal(t, "Golang", profile.Tags[0].Name)
			},
		},
		{
			name: "Профиль не найден",
			mockSetup: func(repo *MockProfileRepo, tagRepo *MockUserTagUpdater) {
				// Имитируем, что профиля в базе нет
				repo.On("GetByUserID", mock.Anything, testUserID).
					Return(nil, nil)
			},
			expectedError: ErrNotFound,
		},
		{
			name: "Ошибка базы данных при получении профиля",
			mockSetup: func(repo *MockProfileRepo, tagRepo *MockUserTagUpdater) {
				// Имитируем разрыв соединения с БД
				repo.On("GetByUserID", mock.Anything, testUserID).
					Return(nil, errors.New("db connection failed"))
			},
			expectedError: errors.New("db connection failed"),
		},
	}

	// Запускаем каждый тест из таблицы
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Инициализируем моки для каждого теста
			mockProfileRepo := new(MockProfileRepo)
			mockTagRepo := new(MockUserTagUpdater)

			// Применяем настройки моков (что они должны вернуть в этом конкретном сценарии)
			tt.mockSetup(mockProfileRepo, mockTagRepo)

			// Создаем сервис, передавая ему наши моки вместо реальной базы!
			_, pc := newProfileCache(t)
			service := NewProfileService(mockProfileRepo, mockTagRepo, pc, "https://s3.local")

			// Вызываем тестируемый метод (Act)
			result, err := service.GetProfile(context.Background(), testUserID)

			// Проверяем результаты (Assert)
			if tt.expectedError != nil {
				// Если ждали ошибку, проверяем что она вернулась (используем assert.ErrorIs или Contains)
				assert.Error(t, err)
				if tt.expectedError == ErrNotFound {
					assert.ErrorIs(t, err, ErrNotFound)
				}
				assert.Nil(t, result)
			} else {
				// Если не ждали ошибку, проверяем что ее нет и данные корректны
				assert.NoError(t, err)
				tt.checkResult(t, result)
			}

			// Проверяем, что все методы у моков, которые мы ожидали, действительно были вызваны
			mockProfileRepo.AssertExpectations(t)
			mockTagRepo.AssertExpectations(t)
		})
	}
}

func TestProfileService_UpdateProfile(t *testing.T) {
	testUserID := int64(1)

	t.Run("обновление полей и тегов", func(t *testing.T) {
		repo := new(MockProfileRepo)
		tagRepo := new(MockUserTagUpdater)

		existing := &domain.Profile{ID: 100, UserID: testUserID, Username: "old"}
		// GetByUserID вызывается дважды: в UpdateProfile и затем в GetProfile.
		repo.On("GetByUserID", mock.Anything, testUserID).Return(existing, nil)
		repo.On("UpdateProfile", mock.Anything, mock.Anything).Return(existing, nil)
		tagRepo.On("UpdateTags", mock.Anything, testUserID, []int64{1, 2}).Return(nil)
		tagRepo.On("GetTagsByUserID", mock.Anything, testUserID).
			Return([]domain.Tag{{ID: 1, Name: "Go"}}, nil)

		_, pc := newProfileCache(t)
		svc := NewProfileService(repo, tagRepo, pc, "https://s3.local")

		newNick := "new"
		resp, err := svc.UpdateProfile(context.Background(), testUserID, dto.UpdateProfileRequest{
			Username: &newNick,
			TagIDs:   []int64{1, 2},
		})

		assert.NoError(t, err)
		assert.Equal(t, "new", resp.Username)
		assert.Len(t, resp.Tags, 1)
		repo.AssertExpectations(t)
		tagRepo.AssertExpectations(t)
	})

	t.Run("без тегов UpdateTags не вызывается", func(t *testing.T) {
		repo := new(MockProfileRepo)
		tagRepo := new(MockUserTagUpdater)

		existing := &domain.Profile{ID: 100, UserID: testUserID, Username: "old"}
		repo.On("GetByUserID", mock.Anything, testUserID).Return(existing, nil)
		repo.On("UpdateProfile", mock.Anything, mock.Anything).Return(existing, nil)
		tagRepo.On("GetTagsByUserID", mock.Anything, testUserID).
			Return([]domain.Tag{}, nil)

		_, pc := newProfileCache(t)
		svc := NewProfileService(repo, tagRepo, pc, "https://s3.local")

		newBio := "hello"
		_, err := svc.UpdateProfile(context.Background(), testUserID, dto.UpdateProfileRequest{
			Bio: &newBio,
		})

		assert.NoError(t, err)
		tagRepo.AssertNotCalled(t, "UpdateTags", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("скрытый или несуществующий тег — неверный ввод", func(t *testing.T) {
		repo := new(MockProfileRepo)
		tagRepo := new(MockUserTagUpdater)

		existing := &domain.Profile{ID: 100, UserID: testUserID, Username: "old"}
		repo.On("GetByUserID", mock.Anything, testUserID).Return(existing, nil)
		repo.On("UpdateProfile", mock.Anything, mock.Anything).Return(existing, nil)
		tagRepo.On("UpdateTags", mock.Anything, testUserID, []int64{99}).
			Return(repoErrTagUnavailable())

		_, pc := newProfileCache(t)
		svc := NewProfileService(repo, tagRepo, pc, "https://s3.local")

		_, err := svc.UpdateProfile(context.Background(), testUserID, dto.UpdateProfileRequest{
			TagIDs: []int64{99},
		})

		assert.ErrorIs(t, err, ErrInvalidInput)
	})

	t.Run("avatar not image", func(t *testing.T) {
		wantErr := repo.ErrFileNotImage

		profileRepo := new(MockProfileRepo)
		tagRepo := new(MockUserTagUpdater)

		existing := &domain.Profile{ID: 100, UserID: testUserID, Username: "old"}
		profileRepo.On("GetByUserID", mock.Anything, testUserID).Return(existing, nil)
		profileRepo.On("UpdateProfile", mock.Anything, mock.Anything).Return(nil, wantErr)

		_, pc := newProfileCache(t)
		svc := NewProfileService(profileRepo, tagRepo, pc, "https://s3.local")

		avatarFileID := "550e8400-e29b-41d4-a716-446655440000"
		_, err := svc.UpdateProfile(context.Background(), testUserID, dto.UpdateProfileRequest{
			AvatarFileID: &avatarFileID,
		})

		assert.ErrorIs(t, err, ErrInvalidInput)
	})
}

func TestProfileService_CacheHitAndInvalidate(t *testing.T) {
	ctx := context.Background()
	testUserID := int64(7)

	repo := new(MockProfileRepo)
	tagRepo := new(MockUserTagUpdater)
	mr, pc := newProfileCache(t)

	existing := &domain.Profile{ID: 200, UserID: testUserID, Username: "neo"}
	repo.On("GetByUserID", mock.Anything, testUserID).Return(existing, nil)
	tagRepo.On("GetTagsByUserID", mock.Anything, testUserID).Return([]domain.Tag{}, nil)

	svc := NewProfileService(repo, tagRepo, pc, "https://s3.local")

	// Первое чтение — промах: грузит из репо и кеширует.
	_, err := svc.GetProfile(ctx, testUserID)
	require.NoError(t, err)
	assert.True(t, mr.Exists(cache.ProfileKey(testUserID)))

	// Второе чтение — из кеша: репозиторий не дёргается повторно.
	_, err = svc.GetProfile(ctx, testUserID)
	require.NoError(t, err)
	repo.AssertNumberOfCalls(t, "GetByUserID", 1)

	// Обновление сбрасывает кеш, финальный GetProfile перечитывает из репо.
	repo.On("UpdateProfile", mock.Anything, mock.Anything).Return(existing, nil)
	newNick := "trinity"
	_, err = svc.UpdateProfile(ctx, testUserID, dto.UpdateProfileRequest{Username: &newNick})
	require.NoError(t, err)
	// 1 (первое чтение) + 1 (внутри UpdateProfile) + 1 (финальный GetProfile после инвалидации).
	repo.AssertNumberOfCalls(t, "GetByUserID", 3)
}

// repoErrTagUnavailable — сентинел репозитория. Хелпер нужен потому, что в
// подтестах локальная переменная repo затеняет одноимённый пакет.
func repoErrTagUnavailable() error { return repo.ErrTagUnavailable }
