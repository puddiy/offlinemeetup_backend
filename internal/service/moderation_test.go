package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo/mocks"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"go.uber.org/mock/gomock"
)

type moderationFixture struct {
	reports  *mocks.MockReportRepository
	meetups  *mocks.MockModerationMeetupRepository
	chats    *mocks.MockModerationChatRepository
	profiles *mocks.MockModerationProfileRepository
	users    *mocks.MockUserMeetupLister
	files    *mocks.MockFileStore
	s3       *fakeS3Deleter
	audit    *recordingAuditSvc
	mcache   *fakeMeetupCache
	pcache   *fakeProfileCache
	ccache   *fakeChatCache
	svc      *ModerationService
}

func setupModerationTest(t *testing.T) *moderationFixture {
	t.Helper()
	ctrl := gomock.NewController(t)
	f := &moderationFixture{
		reports:  mocks.NewMockReportRepository(ctrl),
		meetups:  mocks.NewMockModerationMeetupRepository(ctrl),
		chats:    mocks.NewMockModerationChatRepository(ctrl),
		profiles: mocks.NewMockModerationProfileRepository(ctrl),
		users:    mocks.NewMockUserMeetupLister(ctrl),
		files:    mocks.NewMockFileStore(ctrl),
		s3:       &fakeS3Deleter{},
		audit:    &recordingAuditSvc{},
		mcache:   &fakeMeetupCache{},
		pcache:   &fakeProfileCache{},
		ccache:   &fakeChatCache{},
	}
	f.svc = NewModerationService(ModerationDeps{
		Reports:      f.reports,
		Meetups:      f.meetups,
		Chats:        f.chats,
		Profiles:     f.profiles,
		UserMeetups:  f.users,
		Files:        f.files,
		S3:           f.s3,
		Audit:        f.audit,
		MeetupCache:  f.mcache,
		ProfileCache: f.pcache,
		ChatCache:    f.ccache,
		S3PublicURL:  "https://cdn.example",
		Log:          slog.New(slog.DiscardHandler),
	})
	return f
}

// expectReportTx — RunInTx просто исполняет замыкание.
func expectReportTx(f *moderationFixture) {
	f.reports.EXPECT().RunInTx(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, fn func(tx bun.Tx) error) error {
			return fn(bun.Tx{})
		})
}

func openReportOf(typ domain.ReportTargetType, targetID int64) *domain.Report {
	return &domain.Report{
		ID: 100, ReporterID: 9, TargetType: typ, TargetID: targetID, TargetOwnerID: 42,
		Reason: domain.ReportReasonSpam, Status: domain.ReportStatusOpen,
	}
}

func expectReport(f *moderationFixture, rp *domain.Report) {
	f.reports.EXPECT().GetByID(gomock.Any(), rp.ID).Return(rp, nil)
}

func TestListReportsClampsAndMaps(t *testing.T) {
	f := setupModerationTest(t)

	f.reports.EXPECT().
		List(gomock.Any(), repo.ReportQuery{Status: "open", Limit: maxReportLimit, Offset: maxReportOffset}).
		Return([]domain.Report{{
			ID: 1, TargetType: domain.ReportTargetMessage, TargetID: 5,
			Reason: domain.ReportReasonHate, Status: domain.ReportStatusOpen,
		}}, 1, nil)

	page, err := f.svc.ListReports(context.Background(), ReportFilter{Status: "open", Limit: 100000, Offset: 999999999})

	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, "message", page.Items[0].TargetType)
	require.Equal(t, "Разжигание ненависти", page.Items[0].ReasonTitle)
}

func TestGetReportBuildsSnapshotFileURL(t *testing.T) {
	f := setupModerationTest(t)
	key := "uploads/x.png"
	rp := openReportOf(domain.ReportTargetUser, 42)
	rp.SnapshotFileKey = &key
	rp.SnapshotText = "alice"
	expectReport(f, rp)

	d, err := f.svc.GetReport(context.Background(), 100)

	require.NoError(t, err)
	require.Equal(t, "https://cdn.example/uploads/x.png", d.SnapshotFileURL)
	require.Equal(t, "alice", d.SnapshotText)
	require.True(t, d.IsOpen())
}

func TestGetReportNotFound(t *testing.T) {
	f := setupModerationTest(t)
	f.reports.EXPECT().GetByID(gomock.Any(), int64(404)).Return(nil, repo.ErrReportNotFound)

	_, err := f.svc.GetReport(context.Background(), 404)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestDismissClosesThisReportAndAudits(t *testing.T) {
	f := setupModerationTest(t)
	expectReport(f, openReportOf(domain.ReportTargetMessage, 500))
	expectReportTx(f)
	f.reports.EXPECT().
		CloseTx(gomock.Any(), gomock.Any(), int64(100), int64(7), domain.ReportStatusDismissed, AuditActionReportDismiss).
		Return(nil)

	require.NoError(t, f.svc.Dismiss(context.Background(), 7, 100, "10.0.0.1"))

	require.Len(t, f.audit.events, 1)
	ev := f.audit.events[0]
	require.Equal(t, AuditActionReportDismiss, ev.Action)
	require.Equal(t, int64(7), ev.AdminID)
	require.Equal(t, "report", ev.TargetType)
	require.Equal(t, "100", ev.TargetID)
	require.Equal(t, "10.0.0.1", ev.IP)
}

// Review Focus #3: закрытая жалоба (двойной клик, или её закрыло действие по
// соседней жалобе на ту же цель) — «уже закрыта», без транзакции и журнала.
func TestActionOnClosedReportIsRejected(t *testing.T) {
	actions := map[string]func(s *ModerationService) error{
		"dismiss":       func(s *ModerationService) error { return s.Dismiss(context.Background(), 7, 100, "") },
		"cancel meetup": func(s *ModerationService) error { return s.CancelMeetup(context.Background(), 7, 100, "") },
		"delete message": func(s *ModerationService) error {
			_, err := s.DeleteMessage(context.Background(), 7, 100, "")
			return err
		},
		"remove avatar": func(s *ModerationService) error { return s.RemoveAvatar(context.Background(), 7, 100, "") },
		"remove cover":  func(s *ModerationService) error { return s.RemoveCover(context.Background(), 7, 100, "") },
	}
	for name, act := range actions {
		t.Run(name, func(t *testing.T) {
			f := setupModerationTest(t)
			rp := openReportOf(domain.ReportTargetMessage, 500)
			rp.Status = domain.ReportStatusResolved
			expectReport(f, rp)

			require.ErrorIs(t, act(f.svc), ErrReportClosed)
			require.Empty(t, f.audit.events)
		})
	}
}

// Действие обязано подходить типу жалобы: «отменить митап» по жалобе на
// сообщение — ошибка ввода, а не отмена чужого митапа с тем же id.
func TestActionTypeMismatchIsInvalidInput(t *testing.T) {
	f := setupModerationTest(t)
	expectReport(f, openReportOf(domain.ReportTargetMessage, 500))

	require.ErrorIs(t, f.svc.CancelMeetup(context.Background(), 7, 100, ""), ErrInvalidInput)
}

func TestCancelMeetupResolvesAllReportsAndInvalidates(t *testing.T) {
	f := setupModerationTest(t)
	expectReport(f, openReportOf(domain.ReportTargetMeetup, 55))
	expectReportTx(f)
	f.meetups.EXPECT().CancelTx(gomock.Any(), gomock.Any(), int64(55)).Return([]int64{42, 3}, nil)
	f.reports.EXPECT().
		ResolveTargetTx(gomock.Any(), gomock.Any(), domain.ReportTargetMeetup, int64(55), int64(7), AuditActionMeetupCancel).
		Return(3, nil)

	require.NoError(t, f.svc.CancelMeetup(context.Background(), 7, 100, ""))

	ev := f.audit.events[0]
	require.Equal(t, AuditActionMeetupCancel, ev.Action)
	require.Equal(t, "meetup", ev.TargetType)
	require.Equal(t, "55", ev.TargetID)
	require.Equal(t, int64(100), ev.Details["report_id"])
	require.Equal(t, 3, ev.Details["resolved_reports"])
	require.Equal(t, []int64{55}, f.mcache.invalidated)
	require.Equal(t, []int64{42, 3}, f.ccache.invalidated)
}

// Review Focus #2: митап уже отменён (создателем или другим модератором).
func TestCancelMeetupAlreadyCancelledIsTargetGone(t *testing.T) {
	f := setupModerationTest(t)
	expectReport(f, openReportOf(domain.ReportTargetMeetup, 55))
	expectReportTx(f)
	f.meetups.EXPECT().CancelTx(gomock.Any(), gomock.Any(), int64(55)).Return(nil, repo.ErrMeetupNotActive)

	require.ErrorIs(t, f.svc.CancelMeetup(context.Background(), 7, 100, ""), ErrTargetGone)
	require.Empty(t, f.audit.events)
	require.Empty(t, f.mcache.invalidated)
}

func TestDeleteMessageReturnsRemovalAndPurgesAttachment(t *testing.T) {
	f := setupModerationTest(t)
	fileID := uuid.New()
	expectReport(f, openReportOf(domain.ReportTargetMessage, 500))
	expectReportTx(f)
	f.chats.EXPECT().DeleteMessageByAdminTx(gomock.Any(), gomock.Any(), int64(500)).
		Return(&repo.DeletedMessage{
			ChatID: 12, FileID: uuid.NullUUID{UUID: fileID, Valid: true}, ParticipantIDs: []int64{42, 9},
		}, nil)
	f.reports.EXPECT().
		ResolveTargetTx(gomock.Any(), gomock.Any(), domain.ReportTargetMessage, int64(500), int64(7), AuditActionMessageDelete).
		Return(1, nil)
	f.files.EXPECT().GetByID(gomock.Any(), fileID).
		Return(&domain.File{ID: fileID, Bucket: "media", Key: "uploads/att.jpg"}, nil)
	f.files.EXPECT().DeleteTx(gomock.Any(), gomock.Nil(), fileID).Return(nil)

	removal, err := f.svc.DeleteMessage(context.Background(), 7, 100, "")

	require.NoError(t, err)
	require.Equal(t, &MessageRemoval{ChatID: 12, MessageID: 500, ParticipantIDs: []int64{42, 9}}, removal)
	require.Equal(t, []string{"media/uploads/att.jpg"}, f.s3.deleted)
	require.Equal(t, []int64{42, 9}, f.ccache.invalidated)
	require.Equal(t, int64(12), f.audit.events[0].Details["chat_id"])
}

// Review Focus #2: автор уже удалил сообщение сам.
func TestDeleteMessageAlreadyDeletedIsTargetGone(t *testing.T) {
	f := setupModerationTest(t)
	expectReport(f, openReportOf(domain.ReportTargetMessage, 500))
	expectReportTx(f)
	f.chats.EXPECT().DeleteMessageByAdminTx(gomock.Any(), gomock.Any(), int64(500)).
		Return(nil, repo.ErrMessageNotFound)

	removal, err := f.svc.DeleteMessage(context.Background(), 7, 100, "")

	require.ErrorIs(t, err, ErrTargetGone)
	require.Nil(t, removal)
	require.Empty(t, f.audit.events)
}

// Объект в S3 удаляется ДО транзакции: к моменту удаления строки он уже
// обязан отсутствовать.
func TestRemoveAvatarDeletesObjectBeforeRow(t *testing.T) {
	f := setupModerationTest(t)
	avatar := uuid.New()
	expectReport(f, openReportOf(domain.ReportTargetUser, 42))
	f.profiles.EXPECT().GetByUserID(gomock.Any(), int64(42)).
		Return(&domain.Profile{UserID: 42, AvatarFileID: uuid.NullUUID{UUID: avatar, Valid: true}}, nil)
	f.files.EXPECT().GetByID(gomock.Any(), avatar).
		Return(&domain.File{ID: avatar, Bucket: "media", Key: "uploads/av.png"}, nil)
	expectReportTx(f)
	f.files.EXPECT().DeleteTx(gomock.Any(), gomock.Any(), avatar).
		DoAndReturn(func(context.Context, bun.IDB, uuid.UUID) error {
			require.Equal(t, []string{"media/uploads/av.png"}, f.s3.deleted,
				"объект удаляется ДО транзакции")
			return nil
		})
	f.reports.EXPECT().
		ResolveTargetTx(gomock.Any(), gomock.Any(), domain.ReportTargetUser, int64(42), int64(7), AuditActionAvatarRemove).
		Return(1, nil)
	f.users.EXPECT().MeetupIDsForUser(gomock.Any(), int64(42)).Return([]int64{5}, nil)

	require.NoError(t, f.svc.RemoveAvatar(context.Background(), 7, 100, ""))

	require.Equal(t, []int64{42}, f.pcache.invalidated)
	require.Equal(t, []int64{5}, f.mcache.invalidated, "снапшоты митапов держат аватар участника")
	require.Equal(t, avatar.String(), f.audit.events[0].Details["file_id"])
}

// Review Focus #4: S3 недоступен — в БД ничего не меняется, в журнале пусто,
// модератор может повторить. Ни RunInTx, ни DeleteTx не ожидаются.
func TestRemoveAvatarS3FailureChangesNothing(t *testing.T) {
	f := setupModerationTest(t)
	f.s3.err = errors.New("s3 down")
	avatar := uuid.New()
	expectReport(f, openReportOf(domain.ReportTargetUser, 42))
	f.profiles.EXPECT().GetByUserID(gomock.Any(), int64(42)).
		Return(&domain.Profile{UserID: 42, AvatarFileID: uuid.NullUUID{UUID: avatar, Valid: true}}, nil)
	f.files.EXPECT().GetByID(gomock.Any(), avatar).
		Return(&domain.File{ID: avatar, Bucket: "media", Key: "k"}, nil)

	err := f.svc.RemoveAvatar(context.Background(), 7, 100, "")

	require.Error(t, err)
	require.NotErrorIs(t, err, ErrTargetGone)
	require.Empty(t, f.audit.events)
	require.Empty(t, f.pcache.invalidated)
}

func TestRemoveAvatarWithoutAvatarIsTargetGone(t *testing.T) {
	f := setupModerationTest(t)
	expectReport(f, openReportOf(domain.ReportTargetUser, 42))
	f.profiles.EXPECT().GetByUserID(gomock.Any(), int64(42)).Return(&domain.Profile{UserID: 42}, nil)

	require.ErrorIs(t, f.svc.RemoveAvatar(context.Background(), 7, 100, ""), ErrTargetGone)
}

// Обложка встроена и в снапшот митапа, и в списки чатов участников.
func TestRemoveCoverInvalidatesMeetupAndChats(t *testing.T) {
	f := setupModerationTest(t)
	cover := uuid.New()
	expectReport(f, openReportOf(domain.ReportTargetMeetup, 55))
	f.meetups.EXPECT().GetByID(gomock.Any(), int64(55), int64(0)).
		Return(&domain.Meetup{
			ID: 55, CoverFileID: uuid.NullUUID{UUID: cover, Valid: true},
			Participants: []*domain.User{{ID: 42}, {ID: 3}},
		}, nil)
	f.files.EXPECT().GetByID(gomock.Any(), cover).
		Return(&domain.File{ID: cover, Bucket: "media", Key: "uploads/cover.jpg"}, nil)
	expectReportTx(f)
	f.files.EXPECT().DeleteTx(gomock.Any(), gomock.Any(), cover).Return(nil)
	f.reports.EXPECT().
		ResolveTargetTx(gomock.Any(), gomock.Any(), domain.ReportTargetMeetup, int64(55), int64(7), AuditActionCoverRemove).
		Return(2, nil)

	require.NoError(t, f.svc.RemoveCover(context.Background(), 7, 100, ""))

	require.Equal(t, []string{"media/uploads/cover.jpg"}, f.s3.deleted)
	require.Equal(t, []int64{55}, f.mcache.invalidated)
	require.Equal(t, []int64{42, 3}, f.ccache.invalidated)
}

// Митапа уже нет (GetByID отдаёт nil, nil) — «контента нет», не паника.
func TestRemoveCoverMissingMeetupIsTargetGone(t *testing.T) {
	f := setupModerationTest(t)
	expectReport(f, openReportOf(domain.ReportTargetMeetup, 55))
	f.meetups.EXPECT().GetByID(gomock.Any(), int64(55), int64(0)).Return(nil, nil)

	require.ErrorIs(t, f.svc.RemoveCover(context.Background(), 7, 100, ""), ErrTargetGone)
}
