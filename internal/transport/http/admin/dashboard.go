package admin

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
)

// AdminStatsSvc — данные дашборда (*service.AdminStatsService).
type AdminStatsSvc interface {
	Dashboard(ctx context.Context) (dto.AdminDashboard, error)
}

// dashboardLoadFailed — текст плашки, когда статистика не загрузилась.
// Причина остаётся в логе: наружу внутренняя ошибка не уходит.
const dashboardLoadFailed = "Не удалось загрузить статистику. Попробуйте обновить страницу через минуту."

// DashboardData — данные шаблона dashboard.
type DashboardData struct {
	Stats   dto.AdminDashboard
	Charts  []DashboardChart
	Actions []DashboardAction
}

// DashboardChart — один график с подписью.
type DashboardChart struct {
	Title  string
	Series dto.DailySeries
	Chart  BarChart
}

// DashboardAction — строка ленты со ссылкой на объект (пустая — ссылки нет).
type DashboardAction struct {
	dto.AdminAuditRow
	Link string
}

// Dashboard — главная страница: плитки, графики, лента действий.
//
// Сбой статистики не роняет страницу: это точка входа в панель, и модератор,
// пришедший разбирать жалобы, должен попасть в меню, даже если тяжёлый запрос
// упал. Отсюда 200 с плашкой, а не 500.
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	admin, _ := middleware.GetAdminFromContext(r.Context())
	page := PageData{Title: "Дашборд", Admin: admin}

	stats, err := h.stats.Dashboard(r.Context())
	if err != nil {
		h.log.Error("loading admin dashboard", slog.Any("error", err))
		page.Error = dashboardLoadFailed
		h.render.Render(w, http.StatusOK, "dashboard", page)
		return
	}

	page.Data = newDashboardData(stats)
	h.render.Render(w, http.StatusOK, "dashboard", page)
}

func newDashboardData(d dto.AdminDashboard) DashboardData {
	s := d.Snapshot
	charts := []DashboardChart{
		{Title: "Регистрации", Series: s.Registrations},
		{Title: "Новые митапы", Series: s.Meetups},
		{Title: "Сообщения", Series: s.Messages},
	}
	for i := range charts {
		charts[i].Chart = newBarChart(charts[i].Series)
	}

	actions := make([]DashboardAction, 0, len(d.RecentActions))
	for _, a := range d.RecentActions {
		actions = append(actions, DashboardAction{AdminAuditRow: a, Link: auditTargetLink(a.TargetType, a.TargetID)})
	}

	return DashboardData{Stats: d, Charts: charts, Actions: actions}
}

// auditTargetLink — страница объекта записи журнала. Пусто, если у объекта
// нет своей страницы (тег, учётка админа) или id не похож на число:
// target_id — свободная строка, и в href из неё попадает только проверенное.
func auditTargetLink(targetType, targetID string) string {
	id, err := strconv.ParseInt(targetID, 10, 64)
	if err != nil || id <= 0 {
		return ""
	}
	switch targetType {
	case "user":
		return "/admin/users/" + targetID
	case "meetup":
		return "/admin/meetups/" + targetID
	case "report":
		return "/admin/reports/" + targetID
	default:
		return ""
	}
}
