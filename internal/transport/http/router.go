package http

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/config"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/websocket"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	_ "github.com/puddingtonnn/offlinemeetup_backend/docs"
	adminTransport "github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/admin"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/handler"
	authMiddleware "github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
	"github.com/redis/go-redis/v9"
	httpSwagger "github.com/swaggo/http-swagger/v2"
)

func NewRouter(authHandler *handler.AuthHandler,
	profileHandler *handler.ProfileHandler,
	meetupHandler *handler.MeetupHandler,
	tagHandler *handler.TagHandler,
	geoHandler *handler.GeoHandler,
	chatHandler *handler.ChatHandler,
	wsHandler *websocket.WSHandler,
	fileHandler *handler.FileHandler,
	adminHandler *adminTransport.Handler,
	statusChecker authMiddleware.UserStatusChecker,
	metricsHandler http.Handler,
	rdb *redis.Client,
	log *slog.Logger,
	cfg *config.Config) *chi.Mux {
	router := chi.NewRouter()

	router.Use(middleware.Logger)
	router.Use(middleware.Recoverer)
	router.Use(authMiddleware.SecurityHeaders)
	router.Use(authMiddleware.BodyLimit(1 << 20)) // 1 MB на JSON-запросы

	router.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	router.Handle("/metrics", metricsHandler)

	// Админ-панель живёт вне /v1: это серверный HTML для браузера, а не
	// версионированное мобильное API, и у неё свой контур авторизации
	// (cookie-сессия + admin_users вместо Bearer + users).
	router.Mount("/admin", adminTransport.Routes(adminHandler, rdb, log, cfg))

	if cfg.Env == "local" || cfg.Env == "dev" {
		router.Post("/auth/dev/login", authHandler.DevLogin)
	}

	router.Route("/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			// Брутфорс/перебор по логину и колбэкам — ограничиваем по IP.
			r.Use(authMiddleware.RateLimiter(rdb, log, "auth", 20, time.Minute, cfg.TrustProxyHeaders))

			r.Post("/auth/google", authHandler.GoogleLogin)
			r.Post("/auth/register", authHandler.Register)
			r.Post("/auth/verify-email", authHandler.VerifyEmail)
			r.Post("/auth/resend-code", authHandler.ResendCode)
			r.Post("/auth/login", authHandler.Login)
			r.Post("/auth/forgot-password", authHandler.ForgotPassword)
			r.Post("/auth/reset-password", authHandler.ResetPassword)
			r.Post("/auth/refresh", authHandler.Refresh)
			r.Post("/auth/logout", authHandler.Logout)
			r.Route("/auth/telegram", func(r chi.Router) {
				r.Get("/login", authHandler.ServeTelegramLoginPage)
				r.Get("/callback", authHandler.TelegramCallBack)
			})
		})

		r.Get("/tags", tagHandler.List)

		r.Route("/meetups", func(r chi.Router) {
			r.Group(func(r chi.Router) {
				r.Use(authMiddleware.UserIdentityMiddleware(cfg))

				r.Get("/", meetupHandler.List)
				r.Get("/{id}", meetupHandler.GetByID)
			})

			r.Group(func(r chi.Router) {
				r.Use(authMiddleware.AuthMiddleware(cfg, statusChecker))

				r.Post("/", meetupHandler.CreateMeetup)
				r.Patch("/{id}", meetupHandler.Update)
				r.Delete("/{id}", meetupHandler.Delete)
				r.Post("/{id}/join", meetupHandler.Join)
				r.Post("/join/{token}", meetupHandler.JoinByToken)
				r.Post("/{id}/leave", meetupHandler.Leave)
				r.Get("/my", meetupHandler.My)
			})
		})

		r.Group(func(r chi.Router) {
			r.Use(authMiddleware.AuthMiddleware(cfg, statusChecker))

			r.Get("/auth/me", authHandler.Me)
			r.Patch("/auth/password", authHandler.ChangePassword)

			r.Route("/profile", func(r chi.Router) {
				r.Get("/", profileHandler.GetMyProfile)
				r.Patch("/", profileHandler.UpdateMyProfile)
				r.Get("/{id}", profileHandler.GetProfileByID)
			})
			r.Get("/geo/suggest", geoHandler.Suggest)

			r.With(authMiddleware.RateLimiter(rdb, log, "upload", 30, time.Minute, cfg.TrustProxyHeaders)).
				Post("/files/upload", fileHandler.Upload)
		})

		r.Route("/chats", func(r chi.Router) {
			r.Use(authMiddleware.AuthMiddleware(cfg, statusChecker))

			r.Get("/", chatHandler.GetUserChats)
			r.Post("/{id}/messages", chatHandler.SendMessage)
			r.Get("/{id}/messages", chatHandler.GetMessages)
			r.Patch("/{id}/messages/{messageId}", chatHandler.EditMessage)
			r.Delete("/{id}/messages/{messageId}", chatHandler.DeleteMessage)
			r.Get("/{id}/presence", chatHandler.GetChatPresence)
		})

		r.Route("/ws", func(r chi.Router) {
			r.Use(authMiddleware.AuthMiddleware(cfg, statusChecker))

			r.Get("/", wsHandler.ServeWs)
		})
	})

	// Swagger раскрывает всю карту API — отдаём его только в dev/local, как и
	// dev-login, чтобы не светить поверхность атаки в проде.
	if cfg.Env == "local" || cfg.Env == "dev" {
		router.Get("/swagger/*", httpSwagger.Handler(
			httpSwagger.URL("/swagger/doc.json"),
		))
	}

	return router
}
