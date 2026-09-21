package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/cache"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/cache/cachemetrics"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/config"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service/mail"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service/mail/mailmetrics"
	transport "github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http"
	adminTransport "github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/admin"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/handler"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/websocket"
	"github.com/redis/go-redis/v9"
	"github.com/uptrace/bun"
)

type App struct {
	cfg    *config.Config
	log    *slog.Logger
	router http.Handler
	DB     *bun.DB
	hub    *websocket.Hub
	rdb    *redis.Client
}

func New(log *slog.Logger, cfg *config.Config, db *bun.DB) *App {
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})

	if err := rdb.Ping(context.Background()).Err(); err != nil {
		log.Error("failed to connect to redis", "err", err)
	}

	// WS broadcasts fan out across instances via Redis Pub/Sub; local delivery
	// happens only in the consumer started in App.Run.
	hub := websocket.NewHub(log, websocket.NewRedisBus(rdb))

	metricsReg := prometheus.NewRegistry()
	cacheMetrics := cachemetrics.New(metricsReg)
	mailMetrics := mailmetrics.New(metricsReg)
	metricsHandler := promhttp.HandlerFor(metricsReg, promhttp.HandlerOpts{})

	cached := cache.NewTimeoutCache(cache.NewRedisCache(rdb, log), cfg.CacheTimeout)
	chatCache := cache.NewChatCache(cached, cacheMetrics, cfg.CacheTTLChats)

	// AWS SDK v2 Config
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(cfg.S3Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.S3AccessKey, cfg.S3SecretKey, "")),
	)
	if err != nil {
		log.Error("Failed to load AWS config", slog.String("error", err.Error()))
		panic(fmt.Errorf("failed to load AWS config: %w", err))
	}

	// S3 Client with modern endpoint configuration
	s3Client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.S3Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.S3Endpoint)
		}
		o.UsePathStyle = true
	})

	userRepo := repo.NewUserRepo(db)
	profileRepo := repo.NewProfileRepo(db)
	tagRepo := repo.NewTagRepo(db)
	chatRepo := repo.NewChatRepo(db)
	meetupRepo := repo.NewMeetupRepo(db, chatRepo)
	fileRepo := repo.NewFileRepo(db)
	refreshRepo := repo.NewRefreshTokenRepo(db)
	credentialsRepo := repo.NewCredentialsRepo(db)
	adminRepo := repo.NewAdminRepo(db)
	userAdminRepo := repo.NewUserAdminRepo(db)
	auditRepo := repo.NewAuditRepo(db)

	// The presence of MAIL_SMTP_HOST decides, not APP_ENV: outside local/dev
	// config.Load already refuses to start without the MAIL_SMTP_* secrets,
	// so there the relay is always used; inside local/dev they are optional
	// and default to logMailer (logs the code instead of sending, see
	// mail.NewLogMailer doc) — but setting them locally now switches to a
	// real relay, which is how you smoke-test actual delivery without
	// flipping APP_ENV (that would also turn off dev-login and Swagger).
	// smtpMailer's error path is only reachable on a malformed (non-empty)
	// value, e.g. a non-numeric port.
	var mailer mail.Mailer
	if cfg.MailSMTPHost == "" {
		mailer = mail.NewLogMailer(log)
	} else {
		smtpMailer, err := mail.NewSMTPMailer(cfg.MailSMTPHost, cfg.MailSMTPPort, cfg.MailSMTPUser, cfg.MailSMTPPassword, cfg.MailFrom)
		if err != nil {
			log.Error("failed to build smtp mailer", slog.String("error", err.Error()))
			panic(fmt.Errorf("failed to build smtp mailer: %w", err))
		}
		mailer = smtpMailer
	}
	authStore := cache.NewRedisAuthStore(rdb, log)
	adminSessions := cache.NewRedisAdminSessionStore(rdb, log)

	authService := service.NewAuthService(userRepo, userRepo, credentialsRepo, refreshRepo, authStore, mailer, mailMetrics, cfg, log)
	profileCache := cache.NewProfileCache(cached, cacheMetrics, cfg.CacheTTLProfile)
	profileService := service.NewProfileService(profileRepo, userRepo, profileCache, cfg.S3PublicURL)
	meetupCache := cache.NewMeetupCache(cached, cacheMetrics, cfg.CacheTTLMeetup)
	meetupService := service.NewMeetupService(meetupRepo, chatCache, meetupCache, cfg.S3PublicURL)
	tagCache := cache.NewTagCache(cached, cacheMetrics, cfg.CacheTTLTags)
	tagService := service.NewTagService(tagRepo, tagCache)
	geoService := service.NewGeoService(cfg.DaDataToken)
	chatService := service.NewChatService(chatRepo, chatCache, cfg.S3PublicURL)
	presenceStore := cache.NewRedisPresenceStore(rdb)
	presenceService := service.NewPresenceService(presenceStore, chatService, profileRepo, cfg.PresenceTTL)
	fileService := service.NewFileService(fileRepo, s3Client, cfg)
	adminAuthService := service.NewAdminAuthService(adminRepo, adminSessions, cfg, log)
	auditService := service.NewAuditService(auditRepo, log)
	adminUserService := service.NewAdminUserService(userAdminRepo, refreshRepo, auditService, profileCache, log)

	authHandler := handler.NewAuthHandler(authService, log)
	profileHandler := handler.NewProfileHandler(profileService, log)
	meetupHandler := handler.NewMeetupHandler(meetupService, log)
	tagHandler := handler.NewTagHandler(tagService, log)
	geoHandler := handler.NewGeoHandler(geoService, log)
	chatHandler := handler.NewChatHandler(chatService, presenceService, hub, log)
	wsHandler := websocket.NewWebSocketHandler(hub, log, chatService, profileService, presenceService, cfg.WSAllowedOrigins)
	fileHandler := handler.NewFileHandler(fileService, cfg.MaxUploadSize, log)

	// Шаблоны разбираются здесь, на старте: битый шаблон обязан валить
	// запуск, а не первый запрос модератора.
	adminRenderer, err := adminTransport.NewRenderer(log)
	if err != nil {
		log.Error("failed to parse admin templates", slog.String("error", err.Error()))
		panic(fmt.Errorf("failed to parse admin templates: %w", err))
	}
	adminHandler := adminTransport.NewHandler(adminAuthService, adminUserService, auditService, adminRenderer, cfg, log)

	router := transport.NewRouter(authHandler, profileHandler, meetupHandler, tagHandler, geoHandler, chatHandler, wsHandler, fileHandler, adminHandler, authService, metricsHandler, rdb, log, cfg)

	return &App{
		cfg:    cfg,
		log:    log,
		router: router,
		DB:     db,
		hub:    hub,
		rdb:    rdb,
	}
}

func (a *App) Run(ctx context.Context) error {
	go a.hub.Run(ctx)

	// Subscribe this instance to the WS broadcast channel before serving so no
	// early broadcast is missed. Local delivery happens ONLY through this
	// consumer, so without it a node accepts WS connections but never delivers
	// anything. Redis is a hard dependency of the chat (same as for caching and
	// rate limiting), so we fail fast instead of serving a silently-degraded node.
	if err := a.hub.StartConsumer(ctx); err != nil {
		return fmt.Errorf("failed to start ws broadcast consumer: %w", err)
	}

	server := &http.Server{
		Addr:    ":" + a.cfg.AppPort,
		Handler: a.router,
	}

	serverErr := make(chan error, 1)
	go func() {
		a.log.Info("Starting server", slog.String("port", a.cfg.AppPort))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return fmt.Errorf("server failed to start: %w", err)
	case <-ctx.Done():
		a.log.Info("Shutting down server", slog.String("port", a.cfg.AppPort))
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server shutdown failed: %w", err)
	}

	// The hub/consumer goroutines stop on ctx cancellation (already done here).
	// Release the shared Redis client so its pool doesn't leak at exit.
	if err := a.rdb.Close(); err != nil {
		a.log.Error("closing redis", slog.Any("err", err))
	}

	return nil
}
