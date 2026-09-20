package admin

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/config"
	mw "github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
	"github.com/redis/go-redis/v9"
)

const loginPath = "/admin/login"

// Routes собирает поддерево админки. Возвращает chi.Router, который
// router.go монтирует под /admin — так вся админская маршрутизация лежит
// рядом со своими хендлерами, а не раздувает общий роутер.
func Routes(h *Handler, rdb *redis.Client, log *slog.Logger, cfg *config.Config) chi.Router {
	r := chi.NewRouter()

	// Статика (HTMX) — до авторизации: это не секрет, а отдавать её только
	// залогиненным значило бы ломать страницу входа.
	//
	// StripPrefix именно "/admin" (без хвостового слэша): chi.Mount НЕ
	// переписывает r.URL.Path, поэтому сюда приходит полный
	// "/admin/static/htmx.min.js", а FileServer должен получить
	// "/static/htmx.min.js" — ровно то, как файл лежит во встроенной FS.
	r.Handle("/static/*", http.StripPrefix("/admin", http.FileServer(StaticFS())))

	// Форма входа. Лимит по IP — тот же RateLimiter, что у /auth/*, в своей
	// области "admin_login": перебор пароля админа стоит дороже, чем
	// пользовательского, поэтому порог ниже (10/мин против 20/мин).
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireSameOrigin(log))
		r.Use(mw.RateLimiter(rdb, log, "admin_login", 10, time.Minute, cfg.TrustProxyHeaders))

		r.Get("/login", h.LoginForm)
		r.Post("/login", h.LoginSubmit)
	})

	// Всё остальное — только с живой сессией.
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireSameOrigin(log))
		r.Use(mw.AdminSession(h.auth, loginPath))

		r.Get("/", h.Dashboard)
		r.Post("/logout", h.Logout)

		// Пример гейта по роли для будущих милстоунов — раздел управления
		// админами будет доступен только роли admin:
		//   r.With(mw.RequireAdminRole(domain.AdminRoleAdmin)).Get("/admins", h.AdminsList)
	})

	return r
}
