package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo"
)

// maxMeetupOffset — верхняя граница пагинационного сдвига для списка митапов.
const maxMeetupOffset = 100_000

type MeetupRepository interface {
	Create(ctx context.Context, meetup *domain.Meetup, chat *domain.Chat, tagIDs []int64) (*domain.Meetup, error)
	GetByID(ctx context.Context, id int64, currentUserID int64) (*domain.Meetup, error)
	GetForAuth(ctx context.Context, id, userID int64) (*repo.MeetupAuth, error)
	GetByInviteToken(ctx context.Context, token uuid.UUID, currentUserID int64) (*domain.Meetup, error)
	List(ctx context.Context, filter repo.MeetupQuery, currentUserID int64) ([]domain.Meetup, error)
	Update(ctx context.Context, meetup *domain.Meetup, tagIDs *[]int64) error
	Delete(ctx context.Context, id int64) ([]int64, error)
	Join(ctx context.Context, meetupID, userID int64) error
	Leave(ctx context.Context, meetupID, userID int64) error
}

// chatCacheInvalidator drops a user's cached chat list. Joining, leaving or
// creating a meetup changes which group chats a user belongs to, so the next
// chat-list read for that user must not be served from a stale cache. Declared
// here, at the consumer, and satisfied by *cache.ChatCache.
type chatCacheInvalidator interface {
	InvalidateUserChats(ctx context.Context, userID int64) error
	InvalidateUserChatsMany(ctx context.Context, userIDs ...int64) error
}

// meetupCache кеширует инвариантный снапшот митапа и сбрасывает его при мутациях
// тела/участников. Per-user IsMember накладывается внутри кеша. Объявлен здесь,
// у потребителя, и удовлетворяется *cache.MeetupCache. load возвращает
// инвариантный DTO (IsMember=false) и список user_id участников.
type meetupCache interface {
	Meetup(ctx context.Context, meetupID, userID int64, load func() (dto.MeetupResponse, []int64, error)) (*dto.MeetupResponse, error)
	InvalidateMeetup(ctx context.Context, meetupID int64) error
}

type MeetupService struct {
	repo        MeetupRepository
	chatCache   chatCacheInvalidator
	meetupCache meetupCache
	s3PublicURL string
}

func NewMeetupService(repo MeetupRepository, chatCache chatCacheInvalidator, meetupCache meetupCache, s3PublicURL string) *MeetupService {
	return &MeetupService{repo: repo, chatCache: chatCache, meetupCache: meetupCache, s3PublicURL: s3PublicURL}
}

// memberIDs собирает user_id участников из доменной модели (не зависит от
// наличия профиля), чтобы по нему вычислять IsMember для конкретного смотрящего.
func memberIDs(m *domain.Meetup) []int64 {
	ids := make([]int64, 0, len(m.Participants))
	for _, u := range m.Participants {
		if u != nil {
			ids = append(ids, u.ID)
		}
	}
	return ids
}

func (s *MeetupService) CreateMeetup(ctx context.Context, userID int64, req dto.CreateMeetupRequest) (*dto.MeetupResponse, error) {
	meetup := &domain.Meetup{
		Title:       req.Title,
		Description: req.Description,
		IsPublic:    req.IsPublic,
		CreatorID:   userID,
		StartTime:   req.StartTime,
		EndTime:     req.EndTime,
		Location: domain.Location{
			Lat: req.Coordinates.Lat,
			Lng: req.Coordinates.Lng,
		},
		AddressText: req.Address,
	}

	if req.CoverFileID != nil && *req.CoverFileID != "" {
		id, err := uuid.Parse(*req.CoverFileID)
		if err != nil {
			return nil, fmt.Errorf("invalid cover_file_id: %w", ErrInvalidInput)
		}
		meetup.CoverFileID = uuid.NullUUID{UUID: id, Valid: true}
	}

	chat := &domain.Chat{
		Type: "group",
	}

	created, err := s.repo.Create(ctx, meetup, chat, req.TagIDs)
	if err != nil {
		return nil, mapMeetupRepoError(err)
	}

	_ = s.chatCache.InvalidateUserChats(ctx, userID) // best-effort; cache layer logs failures

	return s.mapToResponse(created), nil
}

func (s *MeetupService) mapToResponse(m *domain.Meetup) *dto.MeetupResponse {
	return mapMeetupToDTO(m, s.s3PublicURL)
}

// mapMeetupRepoError переводит инфра-sentinel'ы репозитория в доменные на границе
// слоёв (как mapChatRepoError), чтобы хендлер вернул 403, а не 500.
func mapMeetupRepoError(err error) error {
	switch {
	case errors.Is(err, repo.ErrFileNotOwned):
		return fmt.Errorf("cover file: %w", ErrForbidden)
	case errors.Is(err, repo.ErrFileNotImage):
		return fmt.Errorf("cover file must be an image: %w", ErrInvalidInput)
	case errors.Is(err, repo.ErrTagUnavailable):
		return fmt.Errorf("tags: %w", ErrInvalidInput)
	}
	return err
}

func (s *MeetupService) GetMeetup(ctx context.Context, id int64, userID int64) (*dto.MeetupResponse, error) {
	// Кешируем инвариантный снапшот (GetByID с currentUserID=0 => без is_member),
	// per-user IsMember накладывает кеш-слой поверх копии.
	resp, err := s.meetupCache.Meetup(ctx, id, userID, func() (dto.MeetupResponse, []int64, error) {
		m, err := s.repo.GetByID(ctx, id, 0)
		if err != nil {
			return dto.MeetupResponse{}, nil, fmt.Errorf("getting meetup: %w", err)
		}
		if m == nil {
			return dto.MeetupResponse{}, nil, fmt.Errorf("meetup %d: %w", id, ErrNotFound)
		}
		return *s.mapToResponse(m), memberIDs(m), nil
	})
	if err != nil {
		return nil, err
	}

	if !resp.IsPublic && !resp.IsMember && resp.CreatorID != userID {
		return nil, ErrForbidden
	}

	return resp, nil
}

func (s *MeetupService) ListMeetups(ctx context.Context, userID int64, filter dto.MeetupFilter) ([]*dto.MeetupResponse, error) {
	// Маппим транспортный DTO в критерий репозитория на границе сервиса: так SQL-
	// слой не зависит от формата HTTP-запроса. Заодно применяем бизнес-клампы.
	q := repo.MeetupQuery{
		Lat:         filter.Lat,
		Lng:         filter.Lng,
		Radius:      filter.Radius,
		Limit:       filter.Limit,
		Offset:      filter.Offset,
		Tags:        filter.Tags,
		OnlyMy:      filter.OnlyMy,
		OnlyCreated: filter.OnlyCreated,
		ExcludeOwn:  filter.ExcludeOwn,
		ShowPast:    filter.ShowPast,
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	if q.Limit > 100 {
		q.Limit = 100
	}
	// Ограничиваем offset: бессмысленно большой сдвиг — это деградация запроса.
	if q.Offset < 0 {
		q.Offset = 0
	}
	if q.Offset > maxMeetupOffset {
		q.Offset = maxMeetupOffset
	}

	meetups, err := s.repo.List(ctx, q, userID)
	if err != nil {
		return nil, err
	}

	response := make([]*dto.MeetupResponse, len(meetups))
	for i, m := range meetups {
		response[i] = s.mapToResponse(&m)
		gateInviteToken(response[i], userID) // токен виден только создателю
	}
	return response, nil
}

func (s *MeetupService) UpdateMeetup(ctx context.Context, userID int64, meetupID int64, req dto.UpdateMeetupRequest) (*dto.MeetupResponse, error) {
	existing, err := s.repo.GetByID(ctx, meetupID, userID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("meetup %d: %w", meetupID, ErrNotFound)
	}
	if existing.CreatorID != userID {
		return nil, ErrForbidden
	}
	// Отменённый митап — история: его чат уже только для чтения, а участники
	// видят его как отменённый. Правка переписала бы то, что они видели, —
	// и после отмены модератором позволила бы автору подменить содержимое.
	// Тот же сентинел, что у JoinMeetup на отменённом митапе (409).
	if existing.Status == "cancelled" {
		return nil, ErrMeetupFinished
	}

	if req.Title != nil {
		existing.Title = *req.Title
	}
	if req.Description != nil {
		existing.Description = *req.Description
	}
	if req.IsPublic != nil {
		// При переходе public→private ротируем инвайт-токен: любой токен,
		// собранный пока митап был публичным, перестаёт работать на вступление.
		if existing.IsPublic && !*req.IsPublic {
			existing.InviteToken = uuid.New()
		}
		existing.IsPublic = *req.IsPublic
	}
	if req.StartTime != nil {
		existing.StartTime = *req.StartTime
	}
	if req.EndTime != nil {
		existing.EndTime = *req.EndTime
	}
	if req.Address != nil {
		existing.AddressText = *req.Address
	}
	if req.Coordinates != nil {
		existing.Location = domain.Location{
			Lat: req.Coordinates.Lat,
			Lng: req.Coordinates.Lng,
		}
	}
	if req.CoverFileID != nil {
		if *req.CoverFileID == "" {
			existing.CoverFileID = uuid.NullUUID{}
		} else {
			id, err := uuid.Parse(*req.CoverFileID)
			if err != nil {
				return nil, fmt.Errorf("invalid cover_file_id: %w", ErrInvalidInput)
			}
			existing.CoverFileID = uuid.NullUUID{UUID: id, Valid: true}
		}
	}

	if err := s.repo.Update(ctx, existing, req.TagIDs); err != nil {
		return nil, mapMeetupRepoError(err)
	}

	_ = s.meetupCache.InvalidateMeetup(ctx, meetupID) // best-effort; cache layer logs failures

	return s.mapToResponse(existing), nil
}

func (s *MeetupService) DeleteMeetup(ctx context.Context, userID int64, meetupID int64) error {
	// Авторизация читает только скаляры — GetForAuth вместо полной гидрации GetByID.
	existing, err := s.repo.GetForAuth(ctx, meetupID, userID)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("meetup %d: %w", meetupID, ErrNotFound)
	}

	if existing.CreatorID != userID {
		return ErrForbidden
	}

	participants, err := s.repo.Delete(ctx, meetupID)
	if err != nil {
		return err
	}

	_ = s.meetupCache.InvalidateMeetup(ctx, meetupID) // best-effort; cache layer logs failures
	// Групповой чат стал read-only — список чатов участников устарел.
	if len(participants) > 0 {
		_ = s.chatCache.InvalidateUserChatsMany(ctx, participants...) // best-effort
	}
	return nil
}

func (s *MeetupService) JoinMeetup(ctx context.Context, userID, meetupID int64) error {
	meetup, err := s.repo.GetForAuth(ctx, meetupID, userID)
	if err != nil {
		return err
	}
	if meetup == nil {
		return ErrNotFound
	}

	// В приватный митап можно вступить только по инвайт-токену.
	if !meetup.IsPublic {
		return ErrForbidden
	}

	if meetup.Status != "active" {
		return ErrMeetupFinished
	}

	if time.Now().After(meetup.EndTime) {
		return ErrMeetupFinished
	}

	if meetup.IsMember {
		return ErrAlreadyExists
	}

	if err := s.repo.Join(ctx, meetupID, userID); err != nil {
		return err
	}

	_ = s.chatCache.InvalidateUserChats(ctx, userID)  // best-effort; cache layer logs failures
	_ = s.meetupCache.InvalidateMeetup(ctx, meetupID) // участники изменились — снапшот устарел
	return nil
}

func (s *MeetupService) LeaveMeetup(ctx context.Context, userID, meetupID int64) error {
	meetup, err := s.repo.GetForAuth(ctx, meetupID, userID)
	if err != nil {
		return err
	}
	if meetup == nil {
		return ErrNotFound
	}

	// Организатор не может покинуть свой митап — только удалить его.
	if meetup.CreatorID == userID {
		return ErrOrganizerCannotLeave
	}

	if err := s.repo.Leave(ctx, meetupID, userID); err != nil {
		return err
	}

	_ = s.chatCache.InvalidateUserChats(ctx, userID)  // best-effort; cache layer logs failures
	_ = s.meetupCache.InvalidateMeetup(ctx, meetupID) // участники изменились — снапшот устарел
	return nil
}

func (s *MeetupService) JoinMeetupByToken(ctx context.Context, userID int64, token string) error {
	inviteUUID, err := uuid.Parse(token)
	if err != nil {
		return fmt.Errorf("invalid token format: %w", ErrInvalidInput)
	}

	meetup, err := s.repo.GetByInviteToken(ctx, inviteUUID, userID)
	if err != nil {
		return err
	}
	if meetup == nil {
		return ErrNotFound
	}

	if meetup.Status != "active" {
		return ErrMeetupFinished
	}

	if time.Now().After(meetup.EndTime) {
		return ErrMeetupFinished
	}

	if meetup.IsMember {
		return ErrAlreadyExists
	}

	if err := s.repo.Join(ctx, meetup.ID, userID); err != nil {
		return err
	}

	_ = s.chatCache.InvalidateUserChats(ctx, userID)   // best-effort; cache layer logs failures
	_ = s.meetupCache.InvalidateMeetup(ctx, meetup.ID) // участники изменились — снапшот устарел
	return nil
}
