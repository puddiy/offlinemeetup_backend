package admin

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
)

// loginFailedMessage — ЕДИНСТВЕННЫЙ текст отказа на входе. Один на все
// причины (нет такой учётки / неверный пароль / учётка выключена): разные
// формулировки превратили бы форму в оракул существования админов.
const loginFailedMessage = "Неверный email или пароль"

func (h *Handler) LoginForm(w http.ResponseWriter, r *http.Request) {
	h.render.Render(w, http.StatusOK, "login", PageData{Title: "Вход"})
}

func (h *Handler) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.render.Render(w, http.StatusBadRequest, "login", PageData{
			Title: "Вход",
			Error: loginFailedMessage,
		})
		return
	}

	email := r.PostFormValue("email")
	password := r.PostFormValue("password")

	token, admin, err := h.auth.Login(r.Context(), email, password)
	if err != nil {
		// Отличаем «не пустили» от «сломалось» только в логе, не в ответе.
		// Тот же принцип, что в response.RespondError: 5xx — Error, 4xx — не
		// шумим, иначе перебор пароля утопит настоящие ошибки.
		if errors.Is(err, service.ErrUnauthorized) {
			// Без email и пароля в логе — только след для расследования
			// перебора; Info, чтобы кампания не топила настоящие Error.
			h.log.Info("admin login rejected", slog.String("ip", h.clientIP(r)))
		} else {
			h.log.Error("admin login failed", "error", err)
		}
		h.render.Render(w, http.StatusUnauthorized, "login", PageData{
			Title: "Вход",
			Error: loginFailedMessage,
		})
		return
	}

	h.setSessionCookie(w, token)
	h.recordAudit(r, service.AuditEvent{
		AdminID:    admin.ID,
		Action:     service.AuditActionLogin,
		TargetType: "admin_user",
		TargetID:   strconv.FormatInt(admin.ID, 10),
		IP:         h.clientIP(r),
	})

	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(middleware.AdminSessionCookieName); err == nil {
		if logoutErr := h.auth.Logout(r.Context(), cookie.Value); logoutErr != nil {
			h.log.Error("admin logout failed", "error", logoutErr)
		}
	}

	if admin, ok := middleware.GetAdminFromContext(r.Context()); ok && admin != nil {
		h.recordAudit(r, service.AuditEvent{
			AdminID:    admin.ID,
			Action:     service.AuditActionLogout,
			TargetType: "admin_user",
			TargetID:   strconv.FormatInt(admin.ID, 10),
			IP:         h.clientIP(r),
		})
	}

	h.clearSessionCookie(w)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}
