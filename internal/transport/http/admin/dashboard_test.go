package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/config"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
	"github.com/stretchr/testify/require"
)

type stubStats struct {
	d   dto.AdminDashboard
	err error
}

func (s *stubStats) Dashboard(context.Context) (dto.AdminDashboard, error) { return s.d, s.err }

func newDashboardHandler(t *testing.T, stats *stubStats) *Handler {
	t.Helper()
	r, err := NewRenderer(slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	return NewHandler(Deps{Auth: &stubAuth{}, Stats: stats, Audit: &recordingAudit{}, Render: r,
		Cfg: &config.Config{Env: "local"}, Log: slog.New(slog.DiscardHandler)})
}

var dashAdmin = &domain.AdminUser{ID: 3, Email: "root@x.io", Role: domain.AdminRoleAdmin}

func dashboardRequest() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	return req.WithContext(context.WithValue(req.Context(), middleware.AdminKey, dashAdmin))
}

// series30 — 30 дней нулей, last — число за сегодняшний день.
func series30(last int) dto.DailySeries {
	counts := make([]int, 30)
	counts[29] = last
	return seriesOf("2026-09-11", counts...)
}

func TestDashboardShowsStatsChartsAndFeed(t *testing.T) {
	oldest := time.Date(2026, 10, 9, 7, 0, 0, 0, time.UTC)
	stats := &stubStats{d: dto.AdminDashboard{
		Snapshot: dto.DashboardSnapshot{
			GeneratedAt:   time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC),
			UsersTotal:    1234,
			Active1d:      56,
			Messages1d:    890,
			Registrations: series30(4),
			Meetups:       series30(0),
			Messages:      series30(12),
		},
		OpenReports:        3,
		OldestOpenReportAt: &oldest,
		RecentActions: []dto.AdminAuditRow{
			{ID: 1, AdminEmail: "mod@x.io", Action: "meetup.cancel", TargetType: "meetup", TargetID: "15", CreatedAt: oldest},
			{ID: 2, AdminEmail: "", Action: "tag.hide", TargetType: "tag", TargetID: "4", CreatedAt: oldest},
		},
	}}
	h := newDashboardHandler(t, stats)

	rec := httptest.NewRecorder()
	h.Dashboard(rec, dashboardRequest())

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	require.Contains(t, body, "1 234", "число с разрядами")
	require.Contains(t, body, "stat-alert", "открытые жалобы подсвечены")
	require.Contains(t, body, `href="/admin/reports"`)

	require.Equal(t, 2, strings.Count(body, `<svg class="chart-plot"`), "ряд из нулей рисуется заглушкой, а не графиком")
	require.Contains(t, body, "Нет данных за 30 дней")
	require.Contains(t, body, "<title>10.10: 12</title>", "подсказка при наведении")

	require.Contains(t, body, "отмена митапа")
	require.Contains(t, body, `href="/admin/meetups/15"`)
	require.Contains(t, body, "удалённый админ")
	require.NotContains(t, body, `href="/admin/tags/4"`, "у тега нет своей страницы")

	require.NotContains(t, body, `class="error"`)
}

func TestDashboardEmptyQueueIsCalm(t *testing.T) {
	h := newDashboardHandler(t, &stubStats{d: dto.AdminDashboard{
		Snapshot: dto.DashboardSnapshot{Registrations: series30(0), Meetups: series30(0), Messages: series30(0)},
	}})

	rec := httptest.NewRecorder()
	h.Dashboard(rec, dashboardRequest())

	body := rec.Body.String()
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, body, "stat-alert")
	require.Contains(t, body, "очередь пуста")
	require.Contains(t, body, "Действий пока нет")
	require.Equal(t, 3, strings.Count(body, "Нет данных за 30 дней"))
}

func TestDashboardStatsFailureStillRendersPage(t *testing.T) {
	h := newDashboardHandler(t, &stubStats{err: errors.New("db down: secret dsn")})

	rec := httptest.NewRecorder()
	h.Dashboard(rec, dashboardRequest())

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, `class="error"`)
	require.Contains(t, body, "Не удалось загрузить статистику")
	require.Contains(t, body, `href="/admin/users"`, "меню на месте — панель остаётся рабочей")
	require.NotContains(t, body, "secret dsn", "текст внутренней ошибки наружу не уходит")
}

func TestAuditTargetLink(t *testing.T) {
	cases := []struct {
		typ, id, want string
	}{
		{"user", "5", "/admin/users/5"},
		{"meetup", "15", "/admin/meetups/15"},
		{"report", "7", "/admin/reports/7"},
		{"tag", "3", ""},
		{"admin_user", "1", ""},
		{"meetup", "abc", ""},
		{"meetup", "", ""},
		{"user", "-1", ""},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, auditTargetLink(tc.typ, tc.id), "%s/%s", tc.typ, tc.id)
	}
}
