package admin

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/config"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
	"github.com/stretchr/testify/require"
)

type stubMeetupSvc struct {
	gotFilter service.AdminMeetupFilter
	page      dto.Page[dto.AdminMeetupRow]
	listErr   error

	detail *dto.AdminMeetupDetail
	getErr error

	gotActor   int64
	gotMeetup  int64
	gotCreate  dto.CreateMeetupRequest
	gotUpdate  dto.UpdateMeetupRequest
	createdID  int64
	createErr  error
	updateErr  error
	createCall int
	updateCall int
}

func (s *stubMeetupSvc) List(_ context.Context, f service.AdminMeetupFilter) (dto.Page[dto.AdminMeetupRow], error) {
	s.gotFilter = f
	return s.page, s.listErr
}

func (s *stubMeetupSvc) Get(_ context.Context, id int64) (*dto.AdminMeetupDetail, error) {
	s.gotMeetup = id
	return s.detail, s.getErr
}

func (s *stubMeetupSvc) CreateOfficial(_ context.Context, actorID int64, req dto.CreateMeetupRequest, _ string) (int64, error) {
	s.createCall++
	s.gotActor, s.gotCreate = actorID, req
	return s.createdID, s.createErr
}

func (s *stubMeetupSvc) UpdateOfficial(_ context.Context, actorID, meetupID int64, req dto.UpdateMeetupRequest, _ string) error {
	s.updateCall++
	s.gotActor, s.gotMeetup, s.gotUpdate = actorID, meetupID, req
	return s.updateErr
}

func newMeetupsHandler(t *testing.T, svc *stubMeetupSvc, mod *stubModeration) *Handler {
	t.Helper()
	r, err := NewRenderer(slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	return NewHandler(Deps{Auth: &stubAuth{}, Meetups: svc, Moderation: mod, Audit: &recordingAudit{}, Render: r,
		Cfg: &config.Config{Env: "local"}, Log: slog.New(slog.DiscardHandler)})
}

var meetupModerator = &domain.AdminUser{ID: 8, Email: "mod@x.io", Role: domain.AdminRoleModerator}

func meetupRequest(method, target, id string, admin *domain.AdminUser) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	ctx := context.WithValue(req.Context(), middleware.AdminKey, admin)
	if id != "" {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", id)
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	}
	return req.WithContext(ctx)
}

func TestMeetupsListPassesFilters(t *testing.T) {
	svc := &stubMeetupSvc{page: dto.NewPage([]dto.AdminMeetupRow{
		{ID: 5, Title: "Го-клуб", Status: "active", IsOfficial: true, StartTime: time.Now()},
	}, 1, 20, 0)}
	h := newMeetupsHandler(t, svc, &stubModeration{})

	rec := httptest.NewRecorder()
	h.MeetupsList(rec, meetupRequest(http.MethodGet, "/admin/meetups?q=club&status=cancelled&official=1", "", meetupModerator))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "club", svc.gotFilter.Search)
	require.Equal(t, "cancelled", svc.gotFilter.Status)
	require.True(t, svc.gotFilter.OnlyOfficial)
	require.Contains(t, rec.Body.String(), "Го-клуб")
	require.NotContains(t, rec.Body.String(), `href="/admin/meetups/new"`, "модератору кнопку публикации не показываем")
}

func TestMeetupsListUnknownStatusIsIgnored(t *testing.T) {
	svc := &stubMeetupSvc{}
	h := newMeetupsHandler(t, svc, &stubModeration{})

	rec := httptest.NewRecorder()
	h.MeetupsList(rec, meetupRequest(http.MethodGet, "/admin/meetups?status=deleted", "", meetupModerator))

	require.Equal(t, "", svc.gotFilter.Status)
}

func TestMeetupDetailNotFound(t *testing.T) {
	svc := &stubMeetupSvc{getErr: service.ErrNotFound}
	h := newMeetupsHandler(t, svc, &stubModeration{})

	rec := httptest.NewRecorder()
	h.MeetupDetail(rec, meetupRequest(http.MethodGet, "/admin/meetups/9", "9", meetupModerator))

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestMeetupDetailShowsCancelButNoEditForModerator(t *testing.T) {
	svc := &stubMeetupSvc{detail: &dto.AdminMeetupDetail{AdminMeetupRow: dto.AdminMeetupRow{
		ID: 9, Title: "Встреча", Status: "active", IsOfficial: true,
	}}}
	h := newMeetupsHandler(t, svc, &stubModeration{})

	rec := httptest.NewRecorder()
	h.MeetupDetail(rec, meetupRequest(http.MethodGet, "/admin/meetups/9", "9", meetupModerator))

	body := rec.Body.String()
	require.Contains(t, body, `action="/admin/meetups/9/cancel"`)
	require.NotContains(t, body, `href="/admin/meetups/9/edit"`)
}

func TestMeetupCancel(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"ok", nil, "/admin/meetups/9?flash=meetup_cancelled"},
		{"already cancelled", service.ErrTargetGone, "/admin/meetups/9?err=meetup_not_active"},
		{"boom", context.DeadlineExceeded, "/admin/meetups/9?err=moderation_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mod := &stubModeration{actErr: tc.err}
			h := newMeetupsHandler(t, &stubMeetupSvc{}, mod)

			rec := httptest.NewRecorder()
			h.MeetupCancel(rec, meetupRequest(http.MethodPost, "/admin/meetups/9/cancel", "9", meetupModerator))

			require.Equal(t, http.StatusSeeOther, rec.Code)
			require.Equal(t, tc.want, rec.Header().Get("Location"))
			require.Equal(t, []string{"cancel-meetup-by-admin"}, mod.calls)
			require.Equal(t, int64(8), mod.gotActor)
			require.Equal(t, int64(9), mod.gotReport)
		})
	}
}
