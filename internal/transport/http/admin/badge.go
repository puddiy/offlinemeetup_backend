package admin

import (
	"fmt"
	"html/template"
)

// badgeSpec — подпись и тон метки.
type badgeSpec struct {
	text string
	tone string
}

// badgeCatalog — человеческие подписи для значений из БД. Одно место на всю
// админку: раньше шаблоны выводили голое «active»/«banned», и каждый экран
// решал сам, показывать ли их как есть.
var badgeCatalog = map[string]map[string]badgeSpec{
	"user_status": {
		"active":   {"активен", "success"},
		"inactive": {"неактивен", "neutral"},
		"banned":   {"заблокирован", "danger"},
	},
	// Вычисленный статус dto.AdminMeetupRow: active — впереди или идёт.
	"meetup_status": {
		"active":    {"активен", "success"},
		"past":      {"прошёл", "neutral"},
		"cancelled": {"отменён", "danger"},
	},
	"report_status": {
		"open":      {"открыта", "warning"},
		"resolved":  {"решена", "success"},
		"dismissed": {"отклонена", "neutral"},
	},
	"report_target": {
		"meetup":  {"митап", "neutral"},
		"message": {"сообщение", "neutral"},
		"user":    {"пользователь", "neutral"},
	},
	"admin_role": {
		"admin":     {"админ", "accent"},
		"moderator": {"модератор", "neutral"},
	},
	// Действия журнала (service.AuditAction*). Новое действие без подписи
	// здесь покажется как есть — серым.
	"audit_action": {
		"admin.login":            {"вход", "neutral"},
		"admin.logout":           {"выход", "neutral"},
		"user.ban":               {"блокировка", "danger"},
		"user.unban":             {"разблокировка", "success"},
		"user.logout_all":        {"отзыв сессий", "warning"},
		"user.delete":            {"удаление аккаунта", "danger"},
		"user.avatar_remove":     {"снят аватар", "warning"},
		"report.dismiss":         {"жалоба отклонена", "neutral"},
		"meetup.cancel":          {"отмена митапа", "danger"},
		"meetup.cover_remove":    {"снята обложка", "warning"},
		"message.delete":         {"удаление сообщения", "danger"},
		"tag.create":             {"новый тег", "accent"},
		"tag.rename":             {"тег переименован", "accent"},
		"tag.hide":               {"тег скрыт", "neutral"},
		"tag.unhide":             {"тег возвращён", "neutral"},
		"meetup.create_official": {"официальный митап", "accent"},
		"meetup.update_official": {"правка официального", "accent"},
	},
	"audit_target": {
		"user":       {"пользователь", "neutral"},
		"meetup":     {"митап", "neutral"},
		"report":     {"жалоба", "neutral"},
		"tag":        {"тег", "neutral"},
		"admin_user": {"админ", "neutral"},
	},
}

// badgeTones — допустимые тона; совпадают с классами .badge-* в admin.css.
var badgeTones = map[string]bool{"neutral": true, "accent": true, "success": true, "warning": true, "danger": true}

// badge рисует метку для значения из каталога. value — any, потому что в
// шаблон приходят именованные строковые типы (domain.UserStatus,
// domain.AdminRole), а text/template не приводит их к string сам.
//
// Неизвестное значение (новое в БД, но не в каталоге) показывается как есть,
// ЭКРАНИРОВАННЫМ и серым: страница не должна падать, а сырое значение из БД —
// становиться разметкой.
func badge(kind string, value any) template.HTML {
	v := fmt.Sprint(value)
	spec, ok := badgeCatalog[kind][v]
	if !ok {
		spec = badgeSpec{text: v, tone: "neutral"}
	}
	return renderBadge(spec.tone, spec.text)
}

// mark — произвольная метка («служебный», «официальный», «удалён»). Тон вне
// списка превращается в neutral: он попадает в атрибут class, и из шаблона
// туда не должно доехать ничего, кроме известного слова.
func mark(tone, text string) template.HTML {
	if !badgeTones[tone] {
		tone = "neutral"
	}
	return renderBadge(tone, text)
}

// renderBadge — единственное место, где собирается HTML метки. tone к этому
// моменту проверен по badgeTones, text экранируется.
func renderBadge(tone, text string) template.HTML {
	return template.HTML(`<span class="badge badge-` + tone + `">` + template.HTMLEscapeString(text) + `</span>`)
}
