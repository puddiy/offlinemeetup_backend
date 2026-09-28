package service

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo"
)

// AdminMeetupRepository — чтение митапов для админки. Удовлетворяется
// *repo.MeetupRepo.
type AdminMeetupRepository interface {
	AdminList(ctx context.Context, q repo.MeetupAdminQuery) ([]domain.Meetup, int, error)
	GetByID(ctx context.Context, id int64, currentUserID int64) (*domain.Meetup, error)
}

// OfficialMeetupWriter — запись митапа через ОБЫЧНЫЙ сервис митапов.
// Удовлетворяется *MeetupService: админ публикует и правит митапы тем же
// путём, что и пользователь, — с проверкой обложки, тегов, созданием чата
// и инвалидацией кэша. Своего пути записи у админки нет намеренно.
type OfficialMeetupWriter interface {
	CreateMeetup(ctx context.Context, userID int64, req dto.CreateMeetupRequest) (*dto.MeetupResponse, error)
	UpdateMeetup(ctx context.Context, userID int64, meetupID int64, req dto.UpdateMeetupRequest) (*dto.MeetupResponse, error)
}

const (
	defaultAdminMeetupLimit = 20
	maxAdminMeetupLimit     = 100
)

// AdminMeetupFilter — фильтр админского списка на языке транспорта.
type AdminMeetupFilter struct {
	Search       string
	Status       string
	OnlyOfficial bool
	Limit        int
	Offset       int
}

// AdminMeetupService — митапы в админке: список и карточка любых митапов,
// публикация и правка ОФИЦИАЛЬНЫХ (создатель — служебный аккаунт).
//
// Запись идёт через OfficialMeetupWriter (обычный MeetupService) с
// userID = systemUserID. Отсюда правило «админ правит только официальные
// митапы» даёт существующая проверка создателя в UpdateMeetup — второй
// копии этой проверки здесь нет.
//
// Журнал пишется ПОСЛЕ успешной записи, вне её транзакции: транзакцией
// владеет MeetupRepo, а публикация и правка обратимы. Сбой журнала
// логируется и не превращает успех в ошибку. Необратимая отмена митапа
// идёт другим путём — ModerationService.CancelMeetupByAdmin, с журналом
// в той же транзакции.
type AdminMeetupService struct {
	repo         AdminMeetupRepository
	writer       OfficialMeetupWriter
	audit        AuditRecorder
	systemUserID int64
	s3PublicURL  string
	log          *slog.Logger
}

func NewAdminMeetupService(r AdminMeetupRepository, w OfficialMeetupWriter, audit AuditRecorder, systemUserID int64, s3PublicURL string, log *slog.Logger) *AdminMeetupService {
	return &AdminMeetupService{repo: r, writer: w, audit: audit, systemUserID: systemUserID, s3PublicURL: s3PublicURL, log: log}
}

// List отдаёт страницу митапов. Лимит и сдвиг зажимаются так же, как в
// списке пользователей.
func (s *AdminMeetupService) List(ctx context.Context, f AdminMeetupFilter) (dto.Page[dto.AdminMeetupRow], error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultAdminMeetupLimit
	}
	if limit > maxAdminMeetupLimit {
		limit = maxAdminMeetupLimit
	}
	offset := min(max(f.Offset, 0), maxMeetupOffset)

	q := repo.MeetupAdminQuery{Search: f.Search, Status: f.Status, Limit: limit, Offset: offset}
	if f.OnlyOfficial {
		q.CreatorID = s.systemUserID
	}

	meetups, total, err := s.repo.AdminList(ctx, q)
	if err != nil {
		return dto.Page[dto.AdminMeetupRow]{}, fmt.Errorf("list meetups: %w", err)
	}

	now := time.Now()
	rows := make([]dto.AdminMeetupRow, 0, len(meetups))
	for i := range meetups {
		rows = append(rows, s.row(&meetups[i], now))
	}
	return dto.NewPage(rows, total, limit, offset), nil
}

// Get отдаёт карточку митапа.
func (s *AdminMeetupService) Get(ctx context.Context, id int64) (*dto.AdminMeetupDetail, error) {
	m, err := s.repo.GetByID(ctx, id, 0)
	if err != nil {
		return nil, fmt.Errorf("get meetup: %w", err)
	}
	if m == nil {
		return nil, ErrNotFound
	}

	d := &dto.AdminMeetupDetail{
		AdminMeetupRow: s.row(m, time.Now()),
		Description:    m.Description,
		Address:        m.AddressText,
		Lat:            m.Location.Lat,
		Lng:            m.Location.Lng,
		CoverURL:       publicURL(s.s3PublicURL, m.CoverFile),
	}
	for _, t := range m.Tags {
		if t != nil {
			d.TagIDs = append(d.TagIDs, t.ID)
		}
	}
	if d.IsOfficial {
		d.InviteToken = m.InviteToken.String()
	}
	return d, nil
}

// CreateOfficial публикует митап от служебного аккаунта. Возвращает id.
func (s *AdminMeetupService) CreateOfficial(ctx context.Context, actorID int64, req dto.CreateMeetupRequest, ip string) (int64, error) {
	if actorID == 0 {
		return 0, ErrInvalidInput
	}
	created, err := s.writer.CreateMeetup(ctx, s.systemUserID, req)
	if err != nil {
		return 0, err
	}
	s.recordAudit(ctx, AuditEvent{
		AdminID:    actorID,
		Action:     AuditActionMeetupCreateOfficial,
		TargetType: "meetup",
		TargetID:   strconv.FormatInt(created.ID, 10),
		IP:         ip,
		Details:    map[string]any{"title": req.Title},
	})
	return created.ID, nil
}

// UpdateOfficial правит официальный митап. Чужой — ErrForbidden (его
// возвращает проверка создателя в MeetupService.UpdateMeetup).
func (s *AdminMeetupService) UpdateOfficial(ctx context.Context, actorID, meetupID int64, req dto.UpdateMeetupRequest, ip string) error {
	if actorID == 0 {
		return ErrInvalidInput
	}
	if _, err := s.writer.UpdateMeetup(ctx, s.systemUserID, meetupID, req); err != nil {
		return err
	}
	details := map[string]any{}
	if req.Title != nil {
		details["title"] = *req.Title
	}
	s.recordAudit(ctx, AuditEvent{
		AdminID:    actorID,
		Action:     AuditActionMeetupUpdateOfficial,
		TargetType: "meetup",
		TargetID:   strconv.FormatInt(meetupID, 10),
		IP:         ip,
		Details:    details,
	})
	return nil
}

// row маппит доменную модель в строку списка.
func (s *AdminMeetupService) row(m *domain.Meetup, now time.Time) dto.AdminMeetupRow {
	row := dto.AdminMeetupRow{
		ID:                m.ID,
		Title:             m.Title,
		Status:            adminMeetupStatus(m, now),
		IsPublic:          m.IsPublic,
		IsOfficial:        m.CreatorID == s.systemUserID,
		StartTime:         m.StartTime,
		EndTime:           m.EndTime,
		CreatorID:         m.CreatorID,
		ParticipantsCount: m.ParticipantsCount,
	}
	if m.Creator != nil && m.Creator.Profile != nil {
		row.CreatorName = domain.DisplayNameOf(m.Creator.Profile.Username, m.Creator.Profile.DisplayName)
	}
	for _, t := range m.Tags {
		if t != nil {
			row.Tags = append(row.Tags, t.Name)
		}
	}
	return row
}

// adminMeetupStatus — статус для человека (см. dto.AdminMeetupRow.Status).
func adminMeetupStatus(m *domain.Meetup, now time.Time) string {
	switch {
	case m.Status == "cancelled":
		return "cancelled"
	case !m.EndTime.After(now):
		return "past"
	default:
		return "active"
	}
}

// recordAudit пишет журнал после уже совершённой обратимой мутации; сбой
// только логируется (см. doc-комментарий AdminMeetupService).
func (s *AdminMeetupService) recordAudit(ctx context.Context, ev AuditEvent) {
	if err := s.audit.Record(ctx, nil, ev); err != nil {
		s.log.Error("recording meetup audit entry",
			slog.String("action", ev.Action), slog.String("meetup_id", ev.TargetID), slog.Any("error", err))
	}
}
