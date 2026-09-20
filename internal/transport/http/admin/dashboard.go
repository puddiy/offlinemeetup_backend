package admin

import (
	"net/http"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
)

// Dashboard — заглушка главной страницы. Содержимое наращивается в
// милстоуне E (метрики); сейчас она подтверждает, что вход и сессия работают.
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	admin, _ := middleware.GetAdminFromContext(r.Context())

	h.render.Render(w, http.StatusOK, "dashboard", PageData{
		Title: "Дашборд",
		Admin: admin,
	})
}
