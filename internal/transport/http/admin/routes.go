package admin

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/config"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	mw "github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
	"github.com/redis/go-redis/v9"
)

const loginPath = "/admin/login"

// Routes собирает поддерево админки. Возвращает chi.Router, который
// router.go монтирует под /admin — так вся админская маршрутизация лежит
// рядом со своими хендлерами, а не раздувает общий роутер.
func Routes(h *Handler, rdb *redis.Client, log *slog.Logger, cfg *config.Config) chi.Router {
	r := chi.NewRouter()

	// Проверка Origin — один choke point на всё поддерево /admin, а не по
	// группам: новая группа, где про неё забыли, иначе молча осталась бы без
	// второго рубежа CSRF. Безопасные методы (в т.ч. GET статики) проходят.
	r.Use(mw.RequireSameOrigin(log))

	// Страницы панели отдаются с Referrer-Policy: same-origin, иначе браузер
	// шлёт на их формы `Origin: null` и RequireSameOrigin режет всё подряд.
	// Подробности — в doc-комментарии SameOriginReferrer.
	r.Use(mw.SameOriginReferrer)

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
	//
	// Лимит стоит ТОЛЬКО на POST: бюджет защищает от перебора пароля, и
	// показ формы (GET) или редирект после выхода не должны его расходовать.
	// За Traefik при TRUST_PROXY_HEADERS=false все клиенты делят один IP-бакет,
	// так что 10 GET в минуту иначе положили бы всю панель 429-ми.
	r.Get("/login", h.LoginForm)
	r.With(mw.RateLimiter(rdb, log, "admin_login", 10, time.Minute, cfg.TrustProxyHeaders)).
		Post("/login", h.LoginSubmit)

	// Всё остальное — только с живой сессией.
	r.Group(func(r chi.Router) {
		r.Use(mw.AdminSession(h.auth, loginPath))

		r.Get("/", h.Dashboard)
		r.Post("/logout", h.Logout)

		r.Get("/users", h.UsersList)
		r.Get("/users/{id}", h.UserDetail)
		r.Post("/users/{id}/ban", h.UserBan)
		r.Post("/users/{id}/unban", h.UserUnban)
		r.Post("/users/{id}/logout-all", h.UserLogoutAll)

		// Удаление — единственное НЕОБРАТИМОЕ действие над пользователем, и
		// единственное, закрытое ролью. Модератору по домену положены разбор
		// жалоб, скрытие контента и бан (см. domain.AdminRoleModerator) —
		// анонимизации аккаунта без права на откат там нет. Бан и отзыв
		// сессий остаются доступны обеим ролям: оба обратимы.
		//
		// Гейт здесь — настоящая защита; скрытая кнопка в шаблоне лишь
		// убирает её с глаз, POST'ом по URL обходится в один запрос.
		r.With(mw.RequireAdminRole(domain.AdminRoleAdmin)).
			Post("/users/{id}/delete", h.UserDelete)
	})

	return r
}
