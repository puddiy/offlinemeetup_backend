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
	defaultReportLimit = 20
	maxReportLimit     = 100
	// maxReportOffset — то же правило, что maxMeetupOffset и maxAdminUserOffset.
	maxReportOffset = 100_000
)

// ReportRepository — жалобы глазами модератора. Удовлетворяется *repo.ReportRepo.
type ReportRepository interface {
	RunInTx(ctx context.Context, fn func(tx bun.Tx) error) error
	List(ctx context.Context, q repo.ReportQuery) ([]domain.Report, int, error)
	GetByID(ctx context.Context, id int64) (*domain.Report, error)
	CloseTx(ctx context.Context, tx bun.IDB, id, adminID int64, status domain.ReportStatus, resolution string) error
	ResolveTargetTx(ctx context.Context, tx bun.IDB, targetType domain.ReportTargetType, targetID, adminID int64, resolution string) (int, error)
}

// ModerationMeetupRepository — удовлетворяется *repo.MeetupRepo.
type ModerationMeetupRepository interface {
	GetByID(ctx context.Context, id int64, currentUserID int64) (*domain.Meetup, error)
	CancelTx(ctx context.Context, tx bun.IDB, meetupID int64) ([]int64, error)
}

// ModerationChatRepository — удовлетворяется *repo.ChatRepo.
type ModerationChatRepository interface {
	DeleteMessageByAdminTx(ctx context.Context, tx bun.IDB, msgID int64) (*repo.DeletedMessage, error)
}

// UserMeetupLister — митапы, в снапшоты которых вложен профиль пользователя.
// Нужен при снятии аватара: MeetupCache хранит Creator и Participants целиком.
// Удовлетворяется *repo.UserAdminRepo.
type UserMeetupLister interface {
	MeetupIDsForUser(ctx context.Context, userID int64) ([]int64, error)
}

// ModerationDeps — зависимости сервиса модерации. Структура, а не
// позиционные аргументы: их больше десятка, и перепутать два интерфейса
// одной формы при позиционной передаче слишком легко.
type ModerationDeps struct {
	Reports      ReportRepository
	Meetups      ModerationMeetupRepository
	Chats        ModerationChatRepository
	UserMeetups  UserMeetupLister
	Files        FileStore
	S3           S3DeleteObjectAPI
	Audit        AuditRecorder
	MeetupCache  adminMeetupCache
	ProfileCache adminProfileCache
	ChatCache    adminChatCache
	S3PublicURL  string
	Log          *slog.Logger
}

// ModerationService — очередь жалоб и действия модератора над контентом.
//
// Каждое действие — ОДНА транзакция: мутация контента, закрытие всех
// открытых жалоб на эту цель и запись в admin_audit_log. Кэш сбрасывается
// после коммита.
type ModerationService struct {
	d ModerationDeps
}

func NewModerationService(d ModerationDeps) *ModerationService {
	return &ModerationService{d: d}
}

// ReportFilter — фильтр очереди на языке транспорта. Пустая строка — без фильтра.
type ReportFilter struct {
	Status     string
	TargetType string
	Limit      int
	Offset     int
}

// MessageRemoval — что нужно транспорту для рассылки messageDeleted.
type MessageRemoval struct {
	ChatID         int64
	MessageID      int64
	ParticipantIDs []int64
}

// ListReports отдаёт страницу очереди.
func (s *ModerationService) ListReports(ctx context.Context, f ReportFilter) (dto.Page[dto.AdminReportRow], error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultReportLimit
	}
	if limit > maxReportLimit {
		limit = maxReportLimit
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > maxReportOffset {
		offset = maxReportOffset
	}

	reports, total, err := s.d.Reports.List(ctx, repo.ReportQuery{
		Status:     f.Status,
		TargetType: f.TargetType,
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		return dto.Page[dto.AdminReportRow]{}, fmt.Errorf("list reports: %w", err)
	}

	rows := make([]dto.AdminReportRow, 0, len(reports))
	for _, rp := range reports {
		rows = append(rows, adminReportRow(rp))
	}
	return dto.NewPage(rows, total, limit, offset), nil
}

func adminReportRow(r domain.Report) dto.AdminReportRow {
	return dto.AdminReportRow{
		ID:            r.ID,
		TargetType:    string(r.TargetType),
		TargetID:      r.TargetID,
		TargetOwnerID: r.TargetOwnerID,
		ReporterID:    r.ReporterID,
		Reason:        string(r.Reason),
		ReasonTitle:   r.Reason.Title(),
		Status:        string(r.Status),
		CreatedAt:     r.CreatedAt,
	}
}

// GetReport отдаёт карточку жалобы со снимком контента.
func (s *ModerationService) GetReport(ctx context.Context, id int64) (*dto.AdminReportDetail, error) {
	rp, err := s.d.Reports.GetByID(ctx, id)
	if errors.Is(err, repo.ErrReportNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get report: %w", err)
	}

	d := &dto.AdminReportDetail{
		AdminReportRow: adminReportRow(*rp),
		Comment:        rp.Comment,
		SnapshotText:   rp.SnapshotText,
		ResolvedBy:     rp.ResolvedBy,
		ResolvedAt:     rp.ResolvedAt,
	}
	if rp.Resolution != nil {
		d.Resolution = *rp.Resolution
	}
	if rp.SnapshotFileKey != nil {
		d.SnapshotFileURL = s.d.S3PublicURL + "/" + *rp.SnapshotFileKey
	}
	return d, nil
}

// Dismiss отклоняет жалобу: контент не трогается, закрывается ТОЛЬКО эта
// жалоба — остальные жалобы на ту же цель могут быть обоснованными.
func (s *ModerationService) Dismiss(ctx context.Context, actorID, reportID int64, ip string) error {
	if actorID == 0 {
		return ErrInvalidInput
	}
	rp, err := s.openReport(ctx, reportID, "")
	if err != nil {
		return err
	}

	err = s.d.Reports.RunInTx(ctx, func(tx bun.Tx) error {
		if err := s.d.Reports.CloseTx(ctx, tx, rp.ID, actorID, domain.ReportStatusDismissed, AuditActionReportDismiss); err != nil {
			return err
		}
		return s.d.Audit.Record(ctx, tx, AuditEvent{
			AdminID:    actorID,
			Action:     AuditActionReportDismiss,
			TargetType: "report",
			TargetID:   strconv.FormatInt(rp.ID, 10),
			IP:         ip,
			Details:    map[string]any{"target_type": string(rp.TargetType), "target_id": rp.TargetID},
		})
	})
	return mapModerationError(err, "dismiss report")
}

// CancelMeetup отменяет митап по жалобе — тем же способом, что и создатель
// (status='cancelled' + чат только для чтения, см. repo.cancelMeetupsTx).
func (s *ModerationService) CancelMeetup(ctx context.Context, actorID, reportID int64, ip string) error {
	if actorID == 0 {
		return ErrInvalidInput
	}
	rp, err := s.openReport(ctx, reportID, domain.ReportTargetMeetup)
	if err != nil {
		return err
	}

	var participants []int64
	err = s.d.Reports.RunInTx(ctx, func(tx bun.Tx) error {
		ids, err := s.d.Meetups.CancelTx(ctx, tx, rp.TargetID)
		if err != nil {
			return err
		}
		participants = ids
		return s.resolveAndAudit(ctx, tx, rp, actorID, AuditActionMeetupCancel, "meetup", ip, nil)
	})
	if err != nil {
		return mapModerationError(err, "cancel meetup")
	}

	s.invalidateMeetup(ctx, rp.TargetID)
	s.invalidateChats(ctx, participants)
	return nil
}

// CancelMeetupByAdmin отменяет митап со страницы митапа — без жалобы.
// Та же транзакция, что у CancelMeetup: отмена, закрытие ВСЕХ открытых
// жалоб на митап (иначе они висели бы в очереди на отменённый митап) и
// запись журнала. details.source отличает это действие от отмены по жалобе.
func (s *ModerationService) CancelMeetupByAdmin(ctx context.Context, actorID, meetupID int64, ip string) error {
	if actorID == 0 || meetupID == 0 {
		return ErrInvalidInput
	}

	var participants []int64
	err := s.d.Reports.RunInTx(ctx, func(tx bun.Tx) error {
		ids, err := s.d.Meetups.CancelTx(ctx, tx, meetupID)
		if err != nil {
			return err
		}
		participants = ids

		n, err := s.d.Reports.ResolveTargetTx(ctx, tx, domain.ReportTargetMeetup, meetupID, actorID, AuditActionMeetupCancel)
		if err != nil {
			return err
		}
		return s.d.Audit.Record(ctx, tx, AuditEvent{
			AdminID:    actorID,
			Action:     AuditActionMeetupCancel,
			TargetType: "meetup",
			TargetID:   strconv.FormatInt(meetupID, 10),
			IP:         ip,
			Details:    map[string]any{"source": "meetup_page", "resolved_reports": n},
		})
	})
	if err != nil {
		return mapModerationError(err, "cancel meetup")
	}

	s.invalidateMeetup(ctx, meetupID)
	s.invalidateChats(ctx, participants)
	return nil
}

// DeleteMessage удаляет сообщение по жалобе и возвращает то, что нужно
// транспорту для рассылки messageDeleted.
//
// Вложение удаляется ПОСЛЕ коммита и best-effort: маппер уже не отдаёт
// вложения удалённых сообщений, срочности нет, и сбой S3 не повод
// откатывать удаление оскорбительного сообщения.
func (s *ModerationService) DeleteMessage(ctx context.Context, actorID, reportID int64, ip string) (*MessageRemoval, error) {
	if actorID == 0 {
		return nil, ErrInvalidInput
	}
	rp, err := s.openReport(ctx, reportID, domain.ReportTargetMessage)
	if err != nil {
		return nil, err
	}

	var deleted *repo.DeletedMessage
	err = s.d.Reports.RunInTx(ctx, func(tx bun.Tx) error {
		d, err := s.d.Chats.DeleteMessageByAdminTx(ctx, tx, rp.TargetID)
		if err != nil {
			return err
		}
		deleted = d
		return s.resolveAndAudit(ctx, tx, rp, actorID, AuditActionMessageDelete, "message", ip,
			map[string]any{"chat_id": d.ChatID})
	})
	if err != nil {
		return nil, mapModerationError(err, "delete message")
	}

	s.invalidateChats(ctx, deleted.ParticipantIDs)
	if deleted.FileID.Valid {
		if err := purgeFile(ctx, s.d.Files, s.d.S3, deleted.FileID.UUID); err != nil {
			s.d.Log.Error("purging attachment of moderated message",
				slog.Int64("message_id", rp.TargetID),
				slog.String("file_id", deleted.FileID.UUID.String()),
				slog.Any("error", err))
		}
	}

	return &MessageRemoval{
		ChatID:         deleted.ChatID,
		MessageID:      rp.TargetID,
		ParticipantIDs: deleted.ParticipantIDs,
	}, nil
}

// RemoveAvatar удаляет аватар пользователя, на который пожаловались.
//
// Снимается файл ИЗ СНИМКА жалобы, а не тот, что прикреплён к профилю сейчас:
// автор мог сменить аватар после жалобы, и тогда по текущему ушёл бы невинный
// новый файл, а оскорбительный остался бы в публичном бакете.
func (s *ModerationService) RemoveAvatar(ctx context.Context, actorID, reportID int64, ip string) error {
	if actorID == 0 {
		return ErrInvalidInput
	}
	rp, err := s.openReport(ctx, reportID, domain.ReportTargetUser)
	if err != nil {
		return err
	}

	file, err := s.reportedFile(ctx, rp)
	if err != nil {
		return err
	}

	if err := s.removeFile(ctx, rp, actorID, ip, file, AuditActionAvatarRemove, "user"); err != nil {
		return err
	}

	// Аватар встроен в профиль и в снапшоты митапов, где пользователь
	// создатель или участник.
	if err := s.d.ProfileCache.InvalidateProfile(ctx, rp.TargetID); err != nil {
		s.d.Log.Error("invalidating profile after avatar removal",
			slog.Int64("user_id", rp.TargetID), slog.Any("error", err))
	}
	ids, err := s.d.UserMeetups.MeetupIDsForUser(ctx, rp.TargetID)
	if err != nil {
		s.d.Log.Error("listing meetups after avatar removal",
			slog.Int64("user_id", rp.TargetID), slog.Any("error", err))
		return nil
	}
	for _, id := range ids {
		s.invalidateMeetup(ctx, id)
	}
	return nil
}

// RemoveCover удаляет обложку митапа, на который пожаловались. Как и у
// аватара, снимается файл из снимка жалобы (см. RemoveAvatar).
func (s *ModerationService) RemoveCover(ctx context.Context, actorID, reportID int64, ip string) error {
	if actorID == 0 {
		return ErrInvalidInput
	}
	rp, err := s.openReport(ctx, reportID, domain.ReportTargetMeetup)
	if err != nil {
		return err
	}

	// Митап нужен для сброса кэша: участники берутся из него.
	m, err := s.d.Meetups.GetByID(ctx, rp.TargetID, 0)
	if err != nil {
		return fmt.Errorf("load meetup: %w", err)
	}
	if m == nil {
		return ErrTargetGone
	}

	file, err := s.reportedFile(ctx, rp)
	if err != nil {
		return err
	}

	if err := s.removeFile(ctx, rp, actorID, ip, file, AuditActionCoverRemove, "meetup"); err != nil {
		return err
	}

	// Обложка встроена в снапшот митапа и в списки чатов участников
	// (ChatResponse несёт митап целиком).
	s.invalidateMeetup(ctx, rp.TargetID)
	s.invalidateChats(ctx, memberIDs(m))
	return nil
}

// reportedFile находит файл, который жалующийся видел в момент жалобы, по
// ключу из снимка. Нет ключа (в момент жалобы файла не было) или файла уже нет
// — ErrTargetGone: снимать нечего.
func (s *ModerationService) reportedFile(ctx context.Context, rp *domain.Report) (*domain.File, error) {
	if rp.SnapshotFileKey == nil {
		return nil, ErrTargetGone
	}
	f, err := s.d.Files.GetByKey(ctx, *rp.SnapshotFileKey)
	if err != nil {
		return nil, mapModerationError(err, "load reported file")
	}
	return f, nil
}

// openReport загружает жалобу и проверяет, что по ней можно действовать:
// она открыта и её тип подходит действию (want == "" — любой тип).
//
// Статус проверяется ДО транзакции ради понятного ответа на двойной клик.
// Гонку двух модераторов закрывает не он, а сама мутация: CancelTx и
// DeleteMessageByAdminTx берут строку FOR UPDATE, и второй получит
// ErrTargetGone.
func (s *ModerationService) openReport(ctx context.Context, id int64, want domain.ReportTargetType) (*domain.Report, error) {
	rp, err := s.d.Reports.GetByID(ctx, id)
	if errors.Is(err, repo.ErrReportNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load report: %w", err)
	}
	if rp.Status != domain.ReportStatusOpen {
		return nil, ErrReportClosed
	}
	if want != "" && rp.TargetType != want {
		return nil, fmt.Errorf("report %d targets %s: %w", id, rp.TargetType, ErrInvalidInput)
	}
	return rp, nil
}

// resolveAndAudit закрывает все открытые жалобы на цель и пишет журнал —
// внутри транзакции действия.
func (s *ModerationService) resolveAndAudit(ctx context.Context, tx bun.IDB, rp *domain.Report, actorID int64, action, targetType, ip string, extra map[string]any) error {
	n, err := s.d.Reports.ResolveTargetTx(ctx, tx, rp.TargetType, rp.TargetID, actorID, action)
	if err != nil {
		return err
	}

	details := map[string]any{"report_id": rp.ID, "resolved_reports": n}
	for k, v := range extra {
		details[k] = v
	}
	return s.d.Audit.Record(ctx, tx, AuditEvent{
		AdminID:    actorID,
		Action:     action,
		TargetType: targetType,
		TargetID:   strconv.FormatInt(rp.TargetID, 10),
		IP:         ip,
		Details:    details,
	})
}

// removeFile снимает файл по жалобе.
//
// Объект в S3 удаляется ДО транзакции, строка files — внутри неё. Сбой S3 —
// ничего не изменилось, модератор повторяет. Сбой транзакции после S3 —
// ссылка ведёт на пустоту, повтор доводит дело (удаление отсутствующего
// ключа S3 считает успехом). Обратный порядок при сбое S3 оставил бы
// оскорбительный файл доступным по публичной ссылке без записи о нём.
// Ссылка из профиля/митапа отвязывается сама: FK на files — ON DELETE SET NULL.
func (s *ModerationService) removeFile(ctx context.Context, rp *domain.Report, actorID int64, ip string, f *domain.File, action, targetType string) error {
	if err := deleteStoredObject(ctx, s.d.S3, f); err != nil {
		return err
	}

	err := s.d.Reports.RunInTx(ctx, func(tx bun.Tx) error {
		if err := s.d.Files.DeleteTx(ctx, tx, f.ID); err != nil {
			return err
		}
		return s.resolveAndAudit(ctx, tx, rp, actorID, action, targetType, ip,
			map[string]any{"file_id": f.ID.String(), "file_key": f.Key})
	})
	return mapModerationError(err, action)
}

// mapModerationError переводит сентинелы репозиториев в сервисные.
func mapModerationError(err error, op string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, repo.ErrReportClosed):
		return ErrReportClosed
	case errors.Is(err, repo.ErrReportNotFound):
		return ErrNotFound
	case errors.Is(err, repo.ErrMeetupNotActive),
		errors.Is(err, repo.ErrMessageNotFound),
		errors.Is(err, repo.ErrFileNotFound):
		return ErrTargetGone
	default:
		return fmt.Errorf("%s: %w", op, err)
	}
}

func (s *ModerationService) invalidateMeetup(ctx context.Context, id int64) {
	if err := s.d.MeetupCache.InvalidateMeetup(ctx, id); err != nil {
		s.d.Log.Error("invalidating meetup after moderation",
			slog.Int64("meetup_id", id), slog.Any("error", err))
	}
}

func (s *ModerationService) invalidateChats(ctx context.Context, userIDs []int64) {
	if len(userIDs) == 0 {
		return
	}
	if err := s.d.ChatCache.InvalidateUserChatsMany(ctx, userIDs...); err != nil {
		s.d.Log.Error("invalidating chat lists after moderation", slog.Any("error", err))
	}
}
