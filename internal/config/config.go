package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	AppPort          string
	DBDSN            string
	GoogleClientID   string
	TelegramBotToken string
	JWTSecret        string
	Env              string
	DaDataToken      string

	// JWTAccessTTL — время жизни короткого access-токена (JWT_ACCESS_TTL,
	// дефолт 15m). JWTRefreshTTL — время жизни refresh-токена (JWT_REFRESH_TTL,
	// дефолт 720h ≈ 30 дней).
	JWTAccessTTL  time.Duration
	JWTRefreshTTL time.Duration

	S3Endpoint  string
	S3Region    string
	S3Bucket    string
	S3AccessKey string
	S3SecretKey string
	S3PublicURL string

	RedisAddr     string
	RedisPassword string
	RedisDB       int

	CacheTimeout    time.Duration
	CacheTTLChats   time.Duration
	CacheTTLTags    time.Duration
	CacheTTLProfile time.Duration
	CacheTTLMeetup  time.Duration

	// PresenceTTL — срок жизни ключа присутствия в Redis; обновляется heartbeat'ом
	// из writePump. Должен быть заметно больше pingPeriod (~54s), чтобы один
	// пропущенный heartbeat не выкинул юзера в offline, но достаточно мал, чтобы
	// упавший инстанс быстро «отпустил» свои соединения.
	PresenceTTL time.Duration

	WSAllowedOrigins []string

	// TrustProxyHeaders — доверять X-Real-IP / X-Forwarded-For при вычислении IP
	// клиента для rate-limit. Включать ТОЛЬКО за доверенным прокси, который сам
	// перезаписывает эти заголовки; иначе клиент подделает их и получит свежий
	// бакет на каждый запрос, обойдя лимит. Дефолт false (берём RemoteAddr).
	TrustProxyHeaders bool

	// MaxUploadSize — максимальный размер загружаемого файла в байтах
	// (MAX_UPLOAD_SIZE, дефолт 100 MB). Применяется и в хендлере (MaxBytesReader),
	// и в FileService.Upload.
	MaxUploadSize int64

	// SMTP-реле для писем регистрации/сброса пароля. В local/dev могут быть
	// пустыми (тогда используется logMailer вместо smtpMailer — выбор в Task 7);
	// вне local/dev обязательны, см. проверку ниже.
	MailSMTPHost     string
	MailSMTPPort     string
	MailSMTPUser     string
	MailSMTPPassword string
	MailFrom         string
	// MailSendTimeout — таймаут одной попытки отправки письма (MAIL_SEND_TIMEOUT,
	// дефолт 10s).
	MailSendTimeout time.Duration

	// EmailCodeTTL — время жизни кода подтверждения email (EMAIL_CODE_TTL,
	// дефолт 15m), совпадает с TTL pending-объекта в Redis (ADR-8).
	// EmailCodeMaxAttempts — максимум неверных попыток ввода кода до отказа
	// (EMAIL_CODE_MAX_ATTEMPTS, дефолт 5).
	EmailCodeTTL         time.Duration
	EmailCodeMaxAttempts int
	// EmailResendCooldown — минимальный интервал между повторными отправками
	// письма с кодом на один email (EMAIL_RESEND_COOLDOWN, дефолт 60s).
	// EmailSendQuotaPerHour — максимум писем на один email в час
	// (EMAIL_SEND_QUOTA_PER_HOUR, дефолт 5).
	EmailResendCooldown   time.Duration
	EmailSendQuotaPerHour int

	// LoginFailLimit — порог неудачных попыток входа до временной блокировки
	// (LOGIN_FAIL_LIMIT, дефолт 10, ADR-13). LoginFailWindow — скользящее окно
	// подсчёта (LOGIN_FAIL_WINDOW, дефолт 15m).
	LoginFailLimit  int
	LoginFailWindow time.Duration

	// AdminSessionTTL — окно ПРОСТОЯ сессии админ-панели (ADMIN_SESSION_TTL,
	// дефолт 8h): каждый запрос продлевает его заново.
	// AdminSessionMaxTTL — АБСОЛЮТНЫЙ потолок с момента входа
	// (ADMIN_SESSION_MAX_TTL, дефолт 24h), который продление не двигает.
	// Нужны оба: без потолка активно используемая сессия жила бы вечно, и
	// украденная cookie не протухала бы никогда.
	AdminSessionTTL    time.Duration
	AdminSessionMaxTTL time.Duration
}

// durEnv reads a duration from env (e.g. "200ms", "5m"); on an empty or
// unparseable value it returns def.
func durEnv(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// int64Env reads an integer (bytes) from env; on an empty or unparseable value
// it returns def.
func int64Env(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

// intEnv reads a plain int from env; on an empty or unparseable value it
// returns def.
func intEnv(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{
		AppPort:          os.Getenv("APP_PORT"),
		DBDSN:            os.Getenv("DB_DSN"),
		GoogleClientID:   os.Getenv("GOOGLE_WEB_CLIENT_ID"),
		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		JWTSecret:        os.Getenv("JWT_SECRET_KEY"),
		Env:              os.Getenv("APP_ENV"),
		DaDataToken:      os.Getenv("DADATA_TOKEN"),

		S3Endpoint:  os.Getenv("S3_ENDPOINT"),
		S3Region:    os.Getenv("S3_REGION"),
		S3Bucket:    os.Getenv("S3_BUCKET"),
		S3AccessKey: os.Getenv("S3_ACCESS_KEY"),
		S3SecretKey: os.Getenv("S3_SECRET_KEY"),
		S3PublicURL: os.Getenv("S3_PUBLIC_URL"),

		RedisAddr:     os.Getenv("REDIS_ADDR"),
		RedisPassword: os.Getenv("REDIS_PASSWORD"),

		MailSMTPHost:     os.Getenv("MAIL_SMTP_HOST"),
		MailSMTPPort:     os.Getenv("MAIL_SMTP_PORT"),
		MailSMTPUser:     os.Getenv("MAIL_SMTP_USER"),
		MailSMTPPassword: os.Getenv("MAIL_SMTP_PASSWORD"),
		MailFrom:         os.Getenv("MAIL_FROM"),
	}

	if cfg.RedisAddr == "" {
		cfg.RedisAddr = "meetuper_redis:6379"
	}
	if dbStr := os.Getenv("REDIS_DB"); dbStr != "" {
		if n, err := strconv.Atoi(dbStr); err == nil {
			cfg.RedisDB = n
		}
	}

	cfg.CacheTimeout = durEnv("CACHE_TIMEOUT", 200*time.Millisecond)
	cfg.CacheTTLChats = durEnv("CACHE_TTL_CHATS", 5*time.Minute)
	cfg.CacheTTLTags = durEnv("CACHE_TTL_TAGS", time.Hour)
	cfg.CacheTTLProfile = durEnv("CACHE_TTL_PROFILE", 10*time.Minute)
	cfg.CacheTTLMeetup = durEnv("CACHE_TTL_MEETUP", 2*time.Minute)
	cfg.PresenceTTL = durEnv("PRESENCE_TTL", 2*time.Minute)
	cfg.MaxUploadSize = int64Env("MAX_UPLOAD_SIZE", 100<<20)
	cfg.TrustProxyHeaders = os.Getenv("TRUST_PROXY_HEADERS") == "true"
	cfg.JWTAccessTTL = durEnv("JWT_ACCESS_TTL", 15*time.Minute)
	cfg.JWTRefreshTTL = durEnv("JWT_REFRESH_TTL", 30*24*time.Hour)

	cfg.MailSendTimeout = durEnv("MAIL_SEND_TIMEOUT", 10*time.Second)
	cfg.EmailCodeTTL = durEnv("EMAIL_CODE_TTL", 15*time.Minute)
	cfg.EmailCodeMaxAttempts = intEnv("EMAIL_CODE_MAX_ATTEMPTS", 5)
	cfg.EmailResendCooldown = durEnv("EMAIL_RESEND_COOLDOWN", 60*time.Second)
	cfg.EmailSendQuotaPerHour = intEnv("EMAIL_SEND_QUOTA_PER_HOUR", 5)
	cfg.LoginFailLimit = intEnv("LOGIN_FAIL_LIMIT", 10)
	cfg.LoginFailWindow = durEnv("LOGIN_FAIL_WINDOW", 15*time.Minute)
	cfg.AdminSessionTTL = durEnv("ADMIN_SESSION_TTL", 8*time.Hour)
	cfg.AdminSessionMaxTTL = durEnv("ADMIN_SESSION_MAX_TTL", 24*time.Hour)

	if origins := os.Getenv("WS_ALLOWED_ORIGINS"); origins != "" {
		for o := range strings.SplitSeq(origins, ",") {
			if o = strings.TrimSpace(o); o != "" {
				cfg.WSAllowedOrigins = append(cfg.WSAllowedOrigins, o)
			}
		}
	}

	if cfg.AppPort == "" {
		cfg.AppPort = "9090"
	}

	if cfg.DBDSN == "" {
		return nil, errors.New("DB_DSN is not set")
	}
	if cfg.GoogleClientID == "" {
		fmt.Println("WARNING: GOOGLE_WEB_CLIENT_ID is not set")
	}
	// Секреты-капабилити обязаны быть заданы: пустой JWT-секрет означает подпись/
	// проверку HMAC-ключом нулевой длины (форж токена под любой userID), пустой
	// токен Telegram вырождает HMAC-ключ в sha256("") (форж подписи). Падаем на
	// старте во всех окружениях, как уже делает проверка DB_DSN выше.
	if cfg.TelegramBotToken == "" {
		return nil, errors.New("TELEGRAM_BOT_TOKEN is not set")
	}
	if cfg.JWTSecret == "" {
		return nil, errors.New("JWT_SECRET_KEY is not set")
	}
	if cfg.Env == "" {
		cfg.Env = "local"
	}
	// SMTP-реле обязательно вне local/dev: там нет logMailer-фолбэка, и пустой
	// хост/креды означают, что письма верификации/сброса пароля молча не
	// уйдут ни при регистрации, ни при восстановлении доступа. В local/dev
	// используется logMailer (Task 7), так что пустые значения там ок — как
	// уже устроено с APP_ENV-gated dev-login/Swagger в router.go.
	// 587 (submission + STARTTLS) is what every relay we'd plausibly use
	// speaks — Gmail, Unisender Go, SES. Defaulted rather than required so a
	// local smoke test only needs host/user/password/from; note that the
	// mailer speaks STARTTLS only, so an implicit-TLS port (465) would need
	// a code change, not just this value.
	if cfg.MailSMTPPort == "" {
		cfg.MailSMTPPort = "587"
	}
	if cfg.Env != "local" && cfg.Env != "dev" {
		if cfg.MailSMTPHost == "" {
			return nil, errors.New("MAIL_SMTP_HOST is not set")
		}
		if cfg.MailSMTPUser == "" {
			return nil, errors.New("MAIL_SMTP_USER is not set")
		}
		if cfg.MailSMTPPassword == "" {
			return nil, errors.New("MAIL_SMTP_PASSWORD is not set")
		}
		if cfg.MailFrom == "" {
			return nil, errors.New("MAIL_FROM is not set")
		}
	}
	if cfg.DaDataToken == "" {
		fmt.Println("WARNING: DADATA_TOKEN is empty")
	}

	return cfg, nil
}
