package admin

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/config"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
	"github.com/stretchr/testify/require"
)

type stubTagSvc struct {
	rows    []dto.AdminTagRow
	listErr error

	calls     []string
	gotActor  int64
	gotTag    int64
	gotName   string
	gotHidden bool
	actErr    error
}

func (s *stubTagSvc) List(context.Context) ([]dto.AdminTagRow, error) { return s.rows, s.listErr }

func (s *stubTagSvc) Create(_ context.Context, actorID int64, name, _ string) error {
	s.calls, s.gotActor, s.gotName = append(s.calls, "create"), actorID, name
	return s.actErr
}

func (s *stubTagSvc) Rename(_ context.Context, actorID, tagID int64, name, _ string) error {
	s.calls, s.gotActor, s.gotTag, s.gotName = append(s.calls, "rename"), actorID, tagID, name
	return s.actErr
}

func (s *stubTagSvc) SetHidden(_ context.Context, actorID, tagID int64, hidden bool, _ string) error {
	s.calls, s.gotActor, s.gotTag, s.gotHidden = append(s.calls, "hidden"), actorID, tagID, hidden
	return s.actErr
}

func newTagsHandler(t *testing.T, svc *stubTagSvc) *Handler {
	t.Helper()
	r, err := NewRenderer(slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	return NewHandler(Deps{Auth: &stubAuth{}, Tags: svc, Audit: &recordingAudit{}, Render: r,
		Cfg: &config.Config{Env: "local"}, Log: slog.New(slog.DiscardHandler)})
}

var tagAdmin = &domain.AdminUser{ID: 3, Email: "root@x.io", Role: domain.AdminRoleAdmin}

// tagForm — POST-форма с id в chi-параметре (пустой id — без параметра).
func tagForm(target, id string, values url.Values) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx := context.WithValue(req.Context(), middleware.AdminKey, tagAdmin)
	if id != "" {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", id)
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	}
	return req.WithContext(ctx)
}

func TestTagsListRendersRowsAndEscapes(t *testing.T) {
	svc := &stubTagSvc{rows: []dto.AdminTagRow{
		{ID: 1, Name: "Спорт", Meetups: 2, Users: 5},
		{ID: 2, Name: "<b>x</b>", IsHidden: true},
	}}
	h := newTagsHandler(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/admin/tags", nil)
	req = req.WithContext(context.WithValue(req.Context(), middleware.AdminKey, tagAdmin))
	rec := httptest.NewRecorder()
	h.TagsList(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "Спорт")
	require.Contains(t, body, `action="/admin/tags/2/unhide"`)
	require.Contains(t, body, `action="/admin/tags/1/hide"`)
	require.NotContains(t, body, "<b>x</b>")
}

func TestTagCreateRedirectsWithNotice(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"ok", nil, "/admin/tags?flash=tag_created"},
		{"bad name", service.ErrInvalidInput, "/admin/tags?err=tag_name_invalid"},
		{"taken", service.ErrAlreadyExists, "/admin/tags?err=tag_name_taken"},
		{"boom", context.DeadlineExceeded, "/admin/tags?err=tag_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &stubTagSvc{actErr: tc.err}
			h := newTagsHandler(t, svc)

			rec := httptest.NewRecorder()
			h.TagCreate(rec, tagForm("/admin/tags", "", url.Values{"name": {"Кино"}}))

			require.Equal(t, http.StatusSeeOther, rec.Code)
			require.Equal(t, tc.want, rec.Header().Get("Location"))
			require.Equal(t, "Кино", svc.gotName)
			require.Equal(t, int64(3), svc.gotActor)
		})
	}
}

func TestTagRenameHideUnhide(t *testing.T) {
	svc := &stubTagSvc{}
	h := newTagsHandler(t, svc)

	rec := httptest.NewRecorder()
	h.TagRename(rec, tagForm("/admin/tags/4/rename", "4", url.Values{"name": {"Кино"}}))
	require.Equal(t, "/admin/tags?flash=tag_renamed", rec.Header().Get("Location"))
	require.Equal(t, int64(4), svc.gotTag)

	rec = httptest.NewRecorder()
	h.TagHide(rec, tagForm("/admin/tags/4/hide", "4", nil))
	require.Equal(t, "/admin/tags?flash=tag_hidden", rec.Header().Get("Location"))
	require.True(t, svc.gotHidden)

	rec = httptest.NewRecorder()
	h.TagUnhide(rec, tagForm("/admin/tags/4/unhide", "4", nil))
	require.Equal(t, "/admin/tags?flash=tag_unhidden", rec.Header().Get("Location"))
	require.False(t, svc.gotHidden)
}

func TestTagNotFoundRedirectsWithError(t *testing.T) {
	svc := &stubTagSvc{actErr: service.ErrNotFound}
	h := newTagsHandler(t, svc)

	rec := httptest.NewRecorder()
	h.TagHide(rec, tagForm("/admin/tags/404/hide", "404", nil))
	require.Equal(t, "/admin/tags?err=tag_not_found", rec.Header().Get("Location"))
}
