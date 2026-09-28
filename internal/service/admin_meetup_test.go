package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo/mocks"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type adminMeetupFixture struct {
	repo   *mocks.MockAdminMeetupRepository
	writer *mocks.MockOfficialMeetupWriter
	audit  *recordingAuditSvc
	svc    *AdminMeetupService
}

func setupAdminMeetupTest(t *testing.T) *adminMeetupFixture {
	t.Helper()
	ctrl := gomock.NewController(t)
	f := &adminMeetupFixture{
		repo:   mocks.NewMockAdminMeetupRepository(ctrl),
		writer: mocks.NewMockOfficialMeetupWriter(ctrl),
		audit:  &recordingAuditSvc{},
	}
	f.svc = NewAdminMeetupService(f.repo, f.writer, f.audit, testSystemUserID, "https://cdn.example", slog.New(slog.DiscardHandler))
	return f
}

func TestCreateOfficialUsesSystemAccountAndAudits(t *testing.T) {
	f := setupAdminMeetupTest(t)
	req := dto.CreateMeetupRequest{Title: "Встреча команды"}
	f.writer.EXPECT().CreateMeetup(gomock.Any(), testSystemUserID, req).
		Return(&dto.MeetupResponse{ID: 77}, nil)

	id, err := f.svc.CreateOfficial(context.Background(), 5, req, "1.2.3.4")

	require.NoError(t, err)
	require.Equal(t, int64(77), id)
	ev := f.audit.events[0]
	require.Equal(t, AuditActionMeetupCreateOfficial, ev.Action)
	require.Equal(t, "meetup", ev.TargetType)
	require.Equal(t, "77", ev.TargetID)
	require.Equal(t, int64(5), ev.AdminID)
}

func TestCreateOfficialErrorIsNotAudited(t *testing.T) {
	f := setupAdminMeetupTest(t)
	f.writer.EXPECT().CreateMeetup(gomock.Any(), testSystemUserID, gomock.Any()).Return(nil, ErrInvalidInput)

	_, err := f.svc.CreateOfficial(context.Background(), 5, dto.CreateMeetupRequest{}, "")

	require.ErrorIs(t, err, ErrInvalidInput)
	require.Empty(t, f.audit.events)
}

// Журнал пишется после коммита митапа: сбой журнала не отменяет уже
// опубликованный митап и не превращает успех в ошибку для админа.
func TestCreateOfficialSurvivesAuditFailure(t *testing.T) {
	f := setupAdminMeetupTest(t)
	f.audit.err = errors.New("audit down")
	f.writer.EXPECT().CreateMeetup(gomock.Any(), testSystemUserID, gomock.Any()).
		Return(&dto.MeetupResponse{ID: 78}, nil)

	id, err := f.svc.CreateOfficial(context.Background(), 5, dto.CreateMeetupRequest{}, "")

	require.NoError(t, err)
	require.Equal(t, int64(78), id)
}

func TestUpdateOfficialOnForeignMeetupIsForbidden(t *testing.T) {
	f := setupAdminMeetupTest(t)
	f.writer.EXPECT().UpdateMeetup(gomock.Any(), testSystemUserID, int64(40), gomock.Any()).Return(nil, ErrForbidden)

	err := f.svc.UpdateOfficial(context.Background(), 5, 40, dto.UpdateMeetupRequest{}, "")

	require.ErrorIs(t, err, ErrForbidden)
	require.Empty(t, f.audit.events)
}

func TestUpdateOfficialAudits(t *testing.T) {
	f := setupAdminMeetupTest(t)
	f.writer.EXPECT().UpdateMeetup(gomock.Any(), testSystemUserID, int64(41), gomock.Any()).
		Return(&dto.MeetupResponse{ID: 41}, nil)

	require.NoError(t, f.svc.UpdateOfficial(context.Background(), 5, 41, dto.UpdateMeetupRequest{}, ""))
	require.Equal(t, AuditActionMeetupUpdateOfficial, f.audit.events[0].Action)
	require.Equal(t, "41", f.audit.events[0].TargetID)
}

func TestAdminMeetupListFiltersAndStatuses(t *testing.T) {
	f := setupAdminMeetupTest(t)
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	f.repo.EXPECT().AdminList(gomock.Any(), repo.MeetupAdminQuery{
		Search: "го", Status: "past", CreatorID: testSystemUserID, Limit: 100, Offset: 0,
	}).Return([]domain.Meetup{
		{ID: 1, Title: "a", Status: "active", EndTime: past, CreatorID: testSystemUserID},
		{ID: 2, Title: "b", Status: "cancelled", EndTime: future, CreatorID: 3,
			Tags: []*domain.Tag{{ID: 4, Name: "Спорт"}}},
		{ID: 3, Title: "c", Status: "active", EndTime: future, CreatorID: 3},
	}, 3, nil)

	page, err := f.svc.List(context.Background(), AdminMeetupFilter{
		Search: "го", Status: "past", OnlyOfficial: true, Limit: 1000, Offset: -5,
	})

	require.NoError(t, err)
	require.Equal(t, 3, page.Total)
	require.Equal(t, "past", page.Items[0].Status)
	require.True(t, page.Items[0].IsOfficial)
	require.Equal(t, "cancelled", page.Items[1].Status)
	require.Equal(t, []string{"Спорт"}, page.Items[1].Tags)
	require.Equal(t, "active", page.Items[2].Status)
	require.False(t, page.Items[2].IsOfficial)
}

func TestAdminMeetupGet(t *testing.T) {
	token := uuid.New()

	t.Run("missing", func(t *testing.T) {
		f := setupAdminMeetupTest(t)
		f.repo.EXPECT().GetByID(gomock.Any(), int64(9), int64(0)).Return(nil, nil)
		_, err := f.svc.Get(context.Background(), 9)
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("official shows invite token", func(t *testing.T) {
		f := setupAdminMeetupTest(t)
		f.repo.EXPECT().GetByID(gomock.Any(), int64(9), int64(0)).Return(&domain.Meetup{
			ID: 9, CreatorID: testSystemUserID, InviteToken: token, Status: "active", EndTime: time.Now().Add(time.Hour),
			Location: domain.Location{Lat: 55.75, Lng: 37.61}, Tags: []*domain.Tag{{ID: 2, Name: "Игры"}},
		}, nil)
		d, err := f.svc.Get(context.Background(), 9)
		require.NoError(t, err)
		require.Equal(t, token.String(), d.InviteToken)
		require.Equal(t, []int64{2}, d.TagIDs)
		require.InDelta(t, 55.75, d.Lat, 1e-9)
	})

	t.Run("foreign hides invite token", func(t *testing.T) {
		f := setupAdminMeetupTest(t)
		f.repo.EXPECT().GetByID(gomock.Any(), int64(9), int64(0)).Return(&domain.Meetup{
			ID: 9, CreatorID: 3, InviteToken: token, Status: "active",
		}, nil)
		d, err := f.svc.Get(context.Background(), 9)
		require.NoError(t, err)
		require.Empty(t, d.InviteToken)
	})
}
