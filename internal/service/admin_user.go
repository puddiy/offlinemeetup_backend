package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/google/uuid"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo"
	"github.com/uptrace/bun"
)

const (
	// defaultAdminUserLimit — размер страницы по умолчанию.
	defaultAdminUserLimit = 20
	// maxAdminUserOffset — потолок сдвига. Без него ?offset=999999999
	// заставляет Postgres просеять и выбросить миллиард строк по одному
	// клику. Значение то же, что у maxMeetupOffset в meetup.go: правило в
	// кодовой базе уже есть, и расходиться с ним незачем.
	maxAdminUserOffset = 100_000
	// maxAdminUserLimit — потолок, чтобы ?limit=100000 не превращался в
	// полный скан таблицы по одному запросу из браузера.
	maxAdminUserLimit = 100
)

// AdminUserRepository — то, что сервис требует от хранилища пользователей.
type AdminUserRepository interface {
	List(ctx context.Context, q repo.UserQuery) ([]domain.User, int, error)
	GetDetail(ctx context.Context, id int64) (*domain.User, error)
	SetStatusTx(ctx context.Context, tx bun.IDB, userID int64, status domain.UserStatus) error
	SoftDeleteTx(ctx context.Context, tx bun.IDB, userID int64) (uuid.NullUUID, error)
	RunInTx(ctx context.Context, fn func(tx bun.Tx) error) error
	MeetupIDsForUser(ctx context.Context, userID int64) ([]int64, error)
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

// adminMeetupCache — узкий срез MeetupCache. Нужен при удалении аккаунта:
// снапшот митапа держит Creator и Participants целиком, поэтому сброса
// одного профиля мало (см. UserAdminRepo.MeetupIDsForUser).
type adminMeetupCache interface {
	InvalidateMeetup(ctx context.Context, meetupID int64) error
}

// adminChatCache — узкий срез ChatCache. У чатов отменённых митапов меняется
// is_read_only, а список чатов кэшируется per-user — без сброса участники ещё
// TTL минут видели бы чат доступным для записи.
type adminChatCache interface {
	InvalidateUserChatsMany(ctx context.Context, userIDs ...int64) error
}

// CreatorMeetupCanceller — отмена активных митапов пользователя при бане и
// удалении аккаунта. Удовлетворяется *repo.MeetupRepo.
type CreatorMeetupCanceller interface {
	CancelActiveByCreatorTx(ctx context.Context, tx bun.IDB, creatorID int64) (repo.CancelledMeetups, error)
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
	repo           AdminUserRepository
	tokens         RefreshTokenRevoker
	audit          AuditRecorder
	profile        adminProfileCache
	meetups        adminMeetupCache
	chats          adminChatCache
	creatorMeetups CreatorMeetupCanceller
	files          FileStore
	s3             S3DeleteObjectAPI
	systemUserID   int64
	log            *slog.Logger
}

func NewAdminUserService(
	r AdminUserRepository,
	tokens RefreshTokenRevoker,
	audit AuditRecorder,
	profile adminProfileCache,
	meetups adminMeetupCache,
	chats adminChatCache,
	creatorMeetups CreatorMeetupCanceller,
	files FileStore,
	s3c S3DeleteObjectAPI,
	systemUserID int64,
	log *slog.Logger,
) *AdminUserService {
	return &AdminUserService{
		repo:           r,
		tokens:         tokens,
		audit:          audit,
		profile:        profile,
		meetups:        meetups,
		chats:          chats,
		creatorMeetups: creatorMeetups,
		files:          files,
		s3:             s3c,
		systemUserID:   systemUserID,
		log:            log,
	}
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
	if offset > maxAdminUserOffset {
		offset = maxAdminUserOffset
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
		IsSystem:  u.IsSystem,
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
	if err := s.guardSystemAccount(userID); err != nil {
		return err
	}

	var cancelled repo.CancelledMeetups
	err := s.repo.RunInTx(ctx, func(tx bun.Tx) error {
		if err := s.repo.SetStatusTx(ctx, tx, userID, status); err != nil {
			return err
		}

		details := map[string]any{"status": string(status)}
		// Бан отменяет будущие и идущие митапы пользователя в той же
		// транзакции (решение продукта от 2026-09-26): иначе в ленте висели
		// бы встречи, на которые организатор уже не придёт. Разбан их НЕ
		// восстанавливает, поэтому для active ветки нет.
		if status == domain.UserStatusBanned {
			c, err := s.creatorMeetups.CancelActiveByCreatorTx(ctx, tx, userID)
			if err != nil {
				return err
			}
			cancelled = c
			details["cancelled_meetups"] = c.MeetupIDs
		}

		return s.audit.Record(ctx, tx, AuditEvent{
			AdminID:    actorID,
			Action:     AuditActionUnbanOrBan(status),
			TargetType: "user",
			TargetID:   strconv.FormatInt(userID, 10),
			IP:         ip,
			Details:    details,
		})
	})
	if errors.Is(err, repo.ErrUserNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("set user status: %w", err)
	}

	s.invalidateProfile(ctx, userID)
	s.invalidateCancelled(ctx, userID, cancelled)
	return nil
}

// invalidateAfterDelete сбрасывает всё, где могла осесть личность удалённого
// пользователя: его профиль и снапшоты митапов, в которые он вложен целиком.
//
// Вызывается ПОСЛЕ коммита и ошибок не возвращает: аккаунт уже удалён,
// откатывать нечего. Но каждый промах логируется — он означает, что имя и
// аватар удалённого человека ещё сколько-то отдаются из кэша.
func (s *AdminUserService) invalidateAfterDelete(ctx context.Context, userID int64) {
	s.invalidateProfile(ctx, userID)

	ids, err := s.repo.MeetupIDsForUser(ctx, userID)
	if err != nil {
		s.log.Error("listing meetups to invalidate after account deletion",
			slog.Int64("user_id", userID), slog.Any("error", err))
		return
	}

	for _, id := range ids {
		if err := s.meetups.InvalidateMeetup(ctx, id); err != nil {
			s.log.Error("invalidating meetup cache after account deletion",
				slog.Int64("user_id", userID), slog.Int64("meetup_id", id), slog.Any("error", err))
		}
	}
}

// invalidateCancelled сбрасывает снапшоты отменённых митапов и списки чатов
// их участников: у чатов поменялся is_read_only. После коммита, best-effort.
func (s *AdminUserService) invalidateCancelled(ctx context.Context, userID int64, c repo.CancelledMeetups) {
	for _, id := range c.MeetupIDs {
		if err := s.meetups.InvalidateMeetup(ctx, id); err != nil {
			s.log.Error("invalidating cancelled meetup cache",
				slog.Int64("user_id", userID), slog.Int64("meetup_id", id), slog.Any("error", err))
		}
	}
	if len(c.ParticipantIDs) > 0 {
		if err := s.chats.InvalidateUserChatsMany(ctx, c.ParticipantIDs...); err != nil {
			s.log.Error("invalidating chat lists after meetup cancellation",
				slog.Int64("user_id", userID), slog.Any("error", err))
		}
	}
}

// afterDelete — всё, что делается после коммита удаления аккаунта. Ошибок не
// возвращает: аккаунт уже удалён, и откатывать нечего. Каждый промах
// логируется: он значит, что личность удалённого ещё видна в кэше или
// его аватар ещё лежит в S3.
func (s *AdminUserService) afterDelete(ctx context.Context, userID int64, avatar uuid.NullUUID, cancelled repo.CancelledMeetups) {
	s.invalidateAfterDelete(ctx, userID)
	s.invalidateCancelled(ctx, userID, cancelled)

	if avatar.Valid {
		if err := purgeFile(ctx, s.files, s.s3, avatar.UUID); err != nil {
			s.log.Error("purging avatar of deleted account",
				slog.Int64("user_id", userID),
				slog.String("file_id", avatar.UUID.String()),
				slog.Any("error", err))
		}
	}
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

// LogoutEverywhere отзывает все refresh-токены пользователя.
//
// Применение: у человека увели телефон, или поддержка закрывает доступ,
// не блокируя аккаунт. Access-токен живёт ещё до 15 минут (JWT_ACCESS_TTL) —
// это цена отсутствия серверного состояния у access-токена. Нужен мгновенный
// отрез — используй бан: AuthMiddleware проверяет статус на каждом запросе.
//
// Отзыв и запись журнала НЕ в одной транзакции: токены живут в своём
// репозитории с собственным соединением. Порядок выбран так, чтобы худший
// исход был безопасным: если журнал не запишется после успешного отзыва,
// мы потеряем строку аудита, но доступ будет отозван. Обратный порядок
// оставил бы запись о действии, которого не произошло.
func (s *AdminUserService) LogoutEverywhere(ctx context.Context, actorID, userID int64, ip string) error {
	if actorID == 0 || userID == 0 {
		return ErrInvalidInput
	}
	if err := s.guardSystemAccount(userID); err != nil {
		return err
	}

	// Проверяем существование ПЕРЕД отзывом. RevokeAllForUser по неизвестному
	// id обновляет ноль строк и не возвращает ошибки, поэтому без этой
	// проверки заход на /admin/users/999999/logout-all (старая закладка,
	// опечатка в URL) давал зелёный флеш «Сессии отозваны» и запись
	// user.logout_all в журнале — след действия, которого не было. Ровно от
	// этого страхует транзакционный аудит в SetStatus и DeleteByAdmin.
	if _, err := s.repo.GetDetail(ctx, userID); err != nil {
		if errors.Is(err, repo.ErrUserNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("logout everywhere: %w", err)
	}

	if err := s.tokens.RevokeAllForUser(ctx, userID); err != nil {
		return fmt.Errorf("revoke refresh tokens: %w", err)
	}

	err := s.repo.RunInTx(ctx, func(tx bun.Tx) error {
		return s.audit.Record(ctx, tx, AuditEvent{
			AdminID:    actorID,
			Action:     AuditActionUserLogout,
			TargetType: "user",
			TargetID:   strconv.FormatInt(userID, 10),
			IP:         ip,
		})
	})
	if err != nil {
		return fmt.Errorf("audit logout everywhere: %w", err)
	}
	return nil
}

// DeleteByAdmin удаляет аккаунт по решению администратора.
//
// Удаление мягкое с анонимизацией (см. UserAdminRepo.SoftDeleteTx): жёсткое
// в этой схеме невозможно — messages.sender_id ссылается на users без
// ON DELETE, и DELETE упадёт на нарушении FK у любого, кто писал в чат.
// Мутация и запись журнала — одной транзакцией.
func (s *AdminUserService) DeleteByAdmin(ctx context.Context, actorID, userID int64, ip string) error {
	if actorID == 0 || userID == 0 {
		return ErrInvalidInput
	}
	if err := s.guardSystemAccount(userID); err != nil {
		return err
	}

	var (
		avatar    uuid.NullUUID
		cancelled repo.CancelledMeetups
	)
	err := s.repo.RunInTx(ctx, func(tx bun.Tx) error {
		a, err := s.repo.SoftDeleteTx(ctx, tx, userID)
		if err != nil {
			return err
		}
		avatar = a

		c, err := s.creatorMeetups.CancelActiveByCreatorTx(ctx, tx, userID)
		if err != nil {
			return err
		}
		cancelled = c

		return s.audit.Record(ctx, tx, AuditEvent{
			AdminID:    actorID,
			Action:     AuditActionUserDelete,
			TargetType: "user",
			TargetID:   strconv.FormatInt(userID, 10),
			IP:         ip,
			Details:    map[string]any{"by": "admin", "cancelled_meetups": c.MeetupIDs},
		})
	})
	if err != nil {
		return s.mapDeleteError(err)
	}

	s.afterDelete(ctx, userID, avatar, cancelled)
	return nil
}

// DeleteOwnAccount удаляет аккаунт по запросу самого пользователя
// (DELETE /v1/account — требование App Store и Google Play).
//
// В admin_audit_log НЕ пишется: у таблицы admin_id NOT NULL, и она про
// действия администраторов. Самоудаление — событие приложения, ему место
// в обычном логе.
func (s *AdminUserService) DeleteOwnAccount(ctx context.Context, userID int64) error {
	if userID == 0 {
		return ErrInvalidInput
	}

	var (
		avatar    uuid.NullUUID
		cancelled repo.CancelledMeetups
	)
	err := s.repo.RunInTx(ctx, func(tx bun.Tx) error {
		a, err := s.repo.SoftDeleteTx(ctx, tx, userID)
		if err != nil {
			return err
		}
		avatar = a

		c, err := s.creatorMeetups.CancelActiveByCreatorTx(ctx, tx, userID)
		if err != nil {
			return err
		}
		cancelled = c
		return nil
	})
	if err != nil {
		return s.mapDeleteError(err)
	}

	s.afterDelete(ctx, userID, avatar, cancelled)
	s.log.Info("account self-deleted",
		slog.Int64("user_id", userID), slog.Int("cancelled_meetups", len(cancelled.MeetupIDs)))
	return nil
}

// mapDeleteError переводит сентинелы репозитория в сервисные.
func (s *AdminUserService) mapDeleteError(err error) error {
	switch {
	case errors.Is(err, repo.ErrUserAlreadyDeleted):
		return ErrUserAlreadyDeleted
	case errors.Is(err, repo.ErrUserNotFound):
		return ErrNotFound
	default:
		return fmt.Errorf("delete account: %w", err)
	}
}

// guardSystemAccount запрещает админские действия над служебным аккаунтом.
// Проверка по id, а не по флагу из БД: она обязана сработать до любой
// мутации и не стоить запроса.
func (s *AdminUserService) guardSystemAccount(userID int64) error {
	if userID == s.systemUserID {
		return ErrSystemAccount
	}
	return nil
}
