package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/config"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/websocket"
	"github.com/stretchr/testify/require"
)

type stubModeration struct {
	gotFilter service.ReportFilter
	page      dto.Page[dto.AdminReportRow]
	listErr   error

	detail *dto.AdminReportDetail
	getErr error

	calls     []string
	gotActor  int64
	gotReport int64
	actErr    error
	removal   *service.MessageRemoval
}

func (s *stubModeration) ListReports(_ context.Context, f service.ReportFilter) (dto.Page[dto.AdminReportRow], error) {
	s.gotFilter = f
	return s.page, s.listErr
}

func (s *stubModeration) GetReport(_ context.Context, id int64) (*dto.AdminReportDetail, error) {
	s.gotReport = id
	return s.detail, s.getErr
}

func (s *stubModeration) act(name string, actorID, reportID int64) error {
	s.calls = append(s.calls, name)
	s.gotActor, s.gotReport = actorID, reportID
	return s.actErr
}

func (s *stubModeration) Dismiss(_ context.Context, a, id int64, _ string) error {
	return s.act("dismiss", a, id)
}

func (s *stubModeration) CancelMeetup(_ context.Context, a, id int64, _ string) error {
	return s.act("cancel-meetup", a, id)
}

func (s *stubModeration) CancelMeetupByAdmin(_ context.Context, a, id int64, _ string) error {
	return s.act("cancel-meetup-by-admin", a, id)
}

func (s *stubModeration) RemoveAvatar(_ context.Context, a, id int64, _ string) error {
	return s.act("remove-avatar", a, id)
}

func (s *stubModeration) RemoveCover(_ context.Context, a, id int64, _ string) error {
	return s.act("remove-cover", a, id)
}

func (s *stubModeration) DeleteMessage(_ context.Context, a, id int64, _ string) (*service.MessageRemoval, error) {
	if err := s.act("delete-message", a, id); err != nil {
		return nil, err
	}
	return s.removal, nil
}

type recordingBroadcaster struct {
	calls   int
	targets []int64
	payload []byte
}

func (b *recordingBroadcaster) BroadcastToUsers(targetIDs []int64, payload []byte) {
	b.calls++
	b.targets, b.payload = targetIDs, payload
}

func newReportsHandler(t *testing.T, mod *stubModeration, ws Broadcaster) *Handler {
	t.Helper()
	r, err := NewRenderer(slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	return NewHandler(Deps{Auth: &stubAuth{}, Moderation: mod, Audit: &recordingAudit{}, Render: r, WS: ws, Cfg: &config.Config{Env: "local"}, Log: slog.New(slog.DiscardHandler)})
}

func reportDetail(typ, status string) *dto.AdminReportDetail {
	return &dto.AdminReportDetail{
		AdminReportRow: dto.AdminReportRow{
			ID: 100, TargetType: typ, TargetID: 500, TargetOwnerID: 42, ReporterID: 9,
			Reason: "spam", ReasonTitle: "Спам или реклама", Status: status, CreatedAt: time.Now(),
		},
		SnapshotText: "купи казино",
	}
}

var moderatorAdmin = &domain.AdminUser{ID: 7, Role: domain.AdminRoleModerator}

func TestReportsListDefaultsToOpenQueue(t *testing.T) {
	mod := &stubModeration{}
	h := newReportsHandler(t, mod, nil)

	rec := httptest.NewRecorder()
	h.ReportsList(rec, usersRequest("/admin/reports"))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "open", mod.gotFilter.Status)
	require.Contains(t, rec.Body.String(), "Жалобы")
}

func TestReportsListAllMeansNoStatusFilter(t *testing.T) {
	mod := &stubModeration{}
	h := newReportsHandler(t, mod, nil)

	rec := httptest.NewRecorder()
	h.ReportsList(rec, usersRequest("/admin/reports?status=all&type=meetup"))

	require.Equal(t, "", mod.gotFilter.Status)
	require.Equal(t, "meetup", mod.gotFilter.TargetType)
	require.Contains(t, rec.Body.String(), `<option value="all" selected>`)
}

func TestReportsListIgnoresUnknownFilters(t *testing.T) {
	mod := &stubModeration{}
	h := newReportsHandler(t, mod, nil)

	rec := httptest.NewRecorder()
	h.ReportsList(rec, usersRequest("/admin/reports?status=drop&type=chat"))

	require.Equal(t, "open", mod.gotFilter.Status)
	require.Equal(t, "", mod.gotFilter.TargetType)
}

// Та же ловушка html/template, что уже ломала пагинацию пользователей.
func TestReportsLinksAreNotOverEscaped(t *testing.T) {
	mod := &stubModeration{page: dto.NewPage([]dto.AdminReportRow{{ID: 1, Status: "open"}}, 47, 20, 20)}
	h := newReportsHandler(t, mod, nil)

	rec := httptest.NewRecorder()
	h.ReportsList(rec, usersRequest("/admin/reports?type=message&offset=20"))

	body := rec.Body.String()
	require.NotContains(t, body, "%26")
	require.NotContains(t, body, "%3d")
	require.Contains(t, body, "offset=40")
	require.Contains(t, body, "offset=0")
	require.Contains(t, body, "type=message")
	require.Contains(t, body, "status=open")
}

func TestReportDetailShowsActionsForType(t *testing.T) {
	cases := []struct {
		typ     string
		want    []string
		notWant []string
	}{
		{"message", []string{"/delete-message"}, []string{"/cancel-meetup", "/remove-cover", "/remove-avatar"}},
		{"meetup", []string{"/cancel-meetup", "/remove-cover"}, []string{"/delete-message", "/remove-avatar"}},
		{"user", []string{"/remove-avatar"}, []string{"/delete-message", "/cancel-meetup", "/remove-cover"}},
	}
	for _, tc := range cases {
		t.Run(tc.typ, func(t *testing.T) {
			h := newReportsHandler(t, &stubModeration{detail: reportDetail(tc.typ, "open")}, nil)

			rec := httptest.NewRecorder()
			h.ReportDetail(rec, getWithChiParam("/admin/reports/100", "100", moderatorAdmin))

			require.Equal(t, http.StatusOK, rec.Code)
			body := rec.Body.String()
			for _, s := range tc.want {
				require.Contains(t, body, s)
			}
			for _, s := range tc.notWant {
				require.NotContains(t, body, s)
			}
			require.Contains(t, body, "/admin/reports/100/dismiss")
			require.Contains(t, body, `href="/admin/users/42"`)
		})
	}
}

func TestReportDetailClosedHasNoActions(t *testing.T) {
	h := newReportsHandler(t, &stubModeration{detail: reportDetail("message", "resolved")}, nil)

	rec := httptest.NewRecorder()
	h.ReportDetail(rec, getWithChiParam("/admin/reports/100", "100", moderatorAdmin))

	body := rec.Body.String()
	require.NotContains(t, body, "/dismiss")
	require.NotContains(t, body, "/delete-message")
}

func TestReportDetailNotFound(t *testing.T) {
	h := newReportsHandler(t, &stubModeration{getErr: service.ErrNotFound}, nil)

	rec := httptest.NewRecorder()
	h.ReportDetail(rec, getWithChiParam("/admin/reports/100", "100", moderatorAdmin))

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "Жалоба не найдена")
}

// Снимок — чужой пользовательский текст; он обязан экранироваться.
func TestReportDetailEscapesSnapshot(t *testing.T) {
	d := reportDetail("message", "open")
	d.SnapshotText = `<script>alert(1)</script>`
	d.Comment = `<img src=x onerror=alert(2)>`
	h := newReportsHandler(t, &stubModeration{detail: d}, nil)

	rec := httptest.NewRecorder()
	h.ReportDetail(rec, getWithChiParam("/admin/reports/100", "100", moderatorAdmin))

	body := rec.Body.String()
	require.NotContains(t, body, "<script>alert(1)</script>")
	require.NotContains(t, body, "<img src=x")
}

// Сквозная проверка каталога: каждое действие и каждая ошибка редиректят
// с ключом, по которому карточка рисует ожидаемый текст в нужной плашке.
func TestReportActionNoticesRoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		action func(h *Handler, w http.ResponseWriter, r *http.Request)
		path   string
		err    error
		banner string
		text   string
	}{
		{"dismiss", (*Handler).ReportDismiss, "dismiss", nil, `class="flash"`, "Жалоба отклонена"},
		{"cancel meetup", (*Handler).ReportCancelMeetup, "cancel-meetup", nil, `class="flash"`, "Митап отменён"},
		{"delete message", (*Handler).ReportDeleteMessage, "delete-message", nil, `class="flash"`, "Сообщение удалено"},
		{"remove avatar", (*Handler).ReportRemoveAvatar, "remove-avatar", nil, `class="flash"`, "Аватар удалён"},
		{"remove cover", (*Handler).ReportRemoveCover, "remove-cover", nil, `class="flash"`, "Обложка удалена"},
		{"closed", (*Handler).ReportDismiss, "dismiss", service.ErrReportClosed, `class="error"`, "уже закрыта"},
		{"gone", (*Handler).ReportCancelMeetup, "cancel-meetup", service.ErrTargetGone, `class="error"`, "Контента уже нет"},
		{"wrong type", (*Handler).ReportRemoveCover, "remove-cover", service.ErrInvalidInput, `class="error"`, "не подходит"},
		{"failure", (*Handler).ReportRemoveAvatar, "remove-avatar", errors.New("boom"), `class="error"`, "Не удалось выполнить"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mod := &stubModeration{actErr: tc.err, detail: reportDetail("message", "open")}
			h := newReportsHandler(t, mod, nil)

			rec := httptest.NewRecorder()
			tc.action(h, rec, postWithChiParam("/admin/reports/100/"+tc.path, "100", moderatorAdmin))
			require.Equal(t, http.StatusSeeOther, rec.Code)
			require.Equal(t, int64(7), mod.gotActor)
			location := rec.Header().Get("Location")

			rec = httptest.NewRecorder()
			h.ReportDetail(rec, getWithChiParam(location, "100", moderatorAdmin))

			body := rec.Body.String()
			require.Contains(t, body, tc.banner, location)
			require.Contains(t, body, tc.text, location)
		})
	}
}

func TestReportActionUnknownReportGoesToList(t *testing.T) {
	h := newReportsHandler(t, &stubModeration{actErr: service.ErrNotFound}, nil)

	rec := httptest.NewRecorder()
	h.ReportDismiss(rec, postWithChiParam("/admin/reports/100/dismiss", "100", moderatorAdmin))

	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/admin/reports", rec.Header().Get("Location"))
}

// Удаление сообщения модератором обязано дойти до открытых клиентов тем же
// событием, что и удаление автором.
func TestReportDeleteMessageBroadcasts(t *testing.T) {
	ws := &recordingBroadcaster{}
	mod := &stubModeration{removal: &service.MessageRemoval{ChatID: 12, MessageID: 500, ParticipantIDs: []int64{42, 9}}}
	h := newReportsHandler(t, mod, ws)

	rec := httptest.NewRecorder()
	h.ReportDeleteMessage(rec, postWithChiParam("/admin/reports/100/delete-message", "100", moderatorAdmin))

	require.Equal(t, 1, ws.calls)
	require.Equal(t, []int64{42, 9}, ws.targets)

	var ev websocket.WSEvent
	require.NoError(t, json.Unmarshal(ws.payload, &ev))
	require.Equal(t, websocket.EventMessageDeleted, ev.Type)
	var p websocket.WSMessageDeletedPayload
	require.NoError(t, json.Unmarshal(ev.Payload, &p))
	require.Equal(t, websocket.WSMessageDeletedPayload{ChatID: 12, MessageID: 500}, p)
}

func TestReportDeleteMessageFailureDoesNotBroadcast(t *testing.T) {
	ws := &recordingBroadcaster{}
	h := newReportsHandler(t, &stubModeration{actErr: service.ErrTargetGone}, ws)

	rec := httptest.NewRecorder()
	h.ReportDeleteMessage(rec, postWithChiParam("/admin/reports/100/delete-message", "100", moderatorAdmin))

	require.Zero(t, ws.calls)
}

// Модерация — работа модератора: гейт по роли на эти маршруты вешать нельзя.
func TestRoutesModerationOpenToModerator(t *testing.T) {
	mod := &stubModeration{}
	root := newRoutesWith(t, domain.AdminRoleModerator, nil, mod)

	req := httptest.NewRequest(http.MethodPost, "http://example.com/admin/reports/100/dismiss", nil)
	req.Header.Set("Origin", "http://example.com")
	req.AddCookie(&http.Cookie{Name: middleware.AdminSessionCookieName, Value: "live-session"})

	rec := serve(root, req)

	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, []string{"dismiss"}, mod.calls)
}
