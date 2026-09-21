package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo"
	"github.com/uptrace/bun"
)

const (
	// defaultAdminUserLimit — размер страницы по умолчанию.
	defaultAdminUserLimit = 20
	// maxAdminUserLimit — потолок, чтобы ?limit=100000 не превращался в
	// полный скан таблицы по одному запросу из браузера.
	maxAdminUserLimit = 100
)

// AdminUserRepository — то, что сервис требует от хранилища пользователей.
type AdminUserRepository interface {
	List(ctx context.Context, q repo.UserQuery) ([]domain.User, int, error)
	GetDetail(ctx context.Context, id int64) (*domain.User, error)
	SetStatusTx(ctx context.Context, tx bun.IDB, userID int64, status domain.UserStatus) error
	SoftDeleteTx(ctx context.Context, tx bun.IDB, userID int64) error
	RunInTx(ctx context.Context, fn func(tx bun.Tx) error) error
}

// RefreshTokenRevoker — отзыв всех refresh-токенов пользователя.
// Уже реализован *repo.RefreshTokenRepo, здесь — узкий потребительский срез.
type RefreshTokenRevoker interface {
	RevokeAllForUser(ctx context.Context, userID int64) error
}

// adminProfileCache — узкий срез ProfileCache. Мутация пользователя обязана
// сбросить его профиль, иначе забаненный ещё TTL минут выглядит активным.
type adminProfileCache interface {
	InvalidateProfile(ctx context.Context, userID int64) error
}

// AdminUserFilter — фильтр админского списка на языке транспорта.
// Сервис маппит его в repo.UserQuery на своей границе, как ListMeetups
// маппит dto.MeetupFilter в repo.MeetupQuery.
type AdminUserFilter struct {
	Search      string
	Status      string
	OnlyDeleted bool
	Limit       int
	Offset      int
}

type AdminUserService struct {
	repo    AdminUserRepository
	tokens  RefreshTokenRevoker
	audit   AuditRecorder
	profile adminProfileCache
	log     *slog.Logger
}

func NewAdminUserService(
	r AdminUserRepository,
	tokens RefreshTokenRevoker,
	audit AuditRecorder,
	profile adminProfileCache,
	log *slog.Logger,
) *AdminUserService {
	return &AdminUserService{repo: r, tokens: tokens, audit: audit, profile: profile, log: log}
}

// ListUsers отдаёт страницу пользователей.
func (s *AdminUserService) ListUsers(ctx context.Context, f AdminUserFilter) (dto.Page[dto.AdminUserRow], error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultAdminUserLimit
	}
	if limit > maxAdminUserLimit {
		limit = maxAdminUserLimit
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	users, total, err := s.repo.List(ctx, repo.UserQuery{
		Search:      f.Search,
		Status:      f.Status,
		OnlyDeleted: f.OnlyDeleted,
		Limit:       limit,
		Offset:      offset,
	})
	if err != nil {
		return dto.Page[dto.AdminUserRow]{}, fmt.Errorf("list users: %w", err)
	}

	rows := make([]dto.AdminUserRow, 0, len(users))
	for _, u := range users {
		rows = append(rows, adminUserRow(u))
	}

	return dto.NewPage(rows, total, limit, offset), nil
}

// GetUser отдаёт пользователя для карточки.
func (s *AdminUserService) GetUser(ctx context.Context, id int64) (*domain.User, error) {
	user, err := s.repo.GetDetail(ctx, id)
	if errors.Is(err, repo.ErrUserNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	return user, nil
}

// adminUserRow маппит доменную модель в строку списка. Профиль может
// отсутствовать (аккаунт заведён, профиль ещё не создан) — тогда поля пустые,
// а не паника на разыменовании nil.
func adminUserRow(u domain.User) dto.AdminUserRow {
	row := dto.AdminUserRow{
		ID:        u.ID,
		Email:     u.Email,
		Status:    string(u.Status),
		CreatedAt: u.CreatedAt,
		DeletedAt: u.DeletedAt,
	}
	if u.Profile != nil {
		row.Username = u.Profile.Username
		row.DisplayName = domain.DisplayNameOf(u.Profile.Username, u.Profile.DisplayName)
	}
	return row
}

// AuditActionUnbanOrBan выбирает действие журнала по целевому статусу.
// Отдельная функция, чтобы транспорт, сервис и тесты называли одно и то же
// действие одинаково, а не собирали строку в трёх местах.
func AuditActionUnbanOrBan(status domain.UserStatus) string {
	if status == domain.UserStatusActive {
		return AuditActionUserUnban
	}
	return AuditActionUserBan
}

// SetStatus меняет статус пользователя и пишет об этом в журнал.
//
// Мутация и запись журнала идут ОДНОЙ транзакцией: если журнал не записался,
// бан откатывается. Иначе в системе появился бы забаненный пользователь без
// следа о том, кто и когда его забанил, — ровно то, ради чего журнал заведён.
//
// Кэш профиля сбрасывается ПОСЛЕ успешного коммита: сбросить раньше значит
// прогреть его снова старым значением, если транзакция откатится.
func (s *AdminUserService) SetStatus(ctx context.Context, actorID, userID int64, status domain.UserStatus, ip string) error {
	switch status {
	case domain.UserStatusActive, domain.UserStatusInactive, domain.UserStatusBanned:
	default:
		return ErrInvalidInput
	}
	if actorID == 0 || userID == 0 {
		return ErrInvalidInput
	}

	err := s.repo.RunInTx(ctx, func(tx bun.Tx) error {
		if err := s.repo.SetStatusTx(ctx, tx, userID, status); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, AuditEvent{
			AdminID:    actorID,
			Action:     AuditActionUnbanOrBan(status),
			TargetType: "user",
			TargetID:   strconv.FormatInt(userID, 10),
			IP:         ip,
			Details:    map[string]any{"status": string(status)},
		})
	})
	if errors.Is(err, repo.ErrUserNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("set user status: %w", err)
	}

	s.invalidateProfile(ctx, userID)
	return nil
}

// invalidateProfile сбрасывает кэш профиля. Ошибка не проваливает запрос:
// мутация уже закоммичена, и откатывать нечего — но она обязана быть видна
// в логах, потому что означает расхождение кэша с БД до истечения TTL.
func (s *AdminUserService) invalidateProfile(ctx context.Context, userID int64) {
	if err := s.profile.InvalidateProfile(ctx, userID); err != nil {
		s.log.Error("invalidating profile cache after admin mutation",
			slog.Int64("user_id", userID), slog.Any("error", err))
	}
}
