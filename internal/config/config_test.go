package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setRequiredSecrets задаёт обязательные секреты, без которых Load теперь падает.
// APP_ENV явно фиксируется в "local", чтобы MAIL_SMTP_* не требовались (см.
// TestLoad_MailSMTPRequiredOutsideLocalDev для проверки обратного случая) —
// и чтобы случайный APP_ENV в окружении процесса не влиял на тест.
func setRequiredSecrets(t *testing.T) {
	t.Helper()
	t.Setenv("JWT_SECRET_KEY", "test-jwt-secret")
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-telegram-token")
	t.Setenv("APP_ENV", "local")
}

func TestLoad_RedisDefaults(t *testing.T) {
	setRequiredSecrets(t)
	t.Setenv("DB_DSN", "postgres://localhost/test")
	t.Setenv("REDIS_ADDR", "")
	t.Setenv("REDIS_PASSWORD", "")
	t.Setenv("REDIS_DB", "")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "meetuper_redis:6379", cfg.RedisAddr)
	assert.Equal(t, "", cfg.RedisPassword)
	assert.Equal(t, 0, cfg.RedisDB)
}

func TestLoad_RedisOverride(t *testing.T) {
	setRequiredSecrets(t)
	t.Setenv("DB_DSN", "postgres://localhost/test")
	t.Setenv("REDIS_ADDR", "localhost:6380")
	t.Setenv("REDIS_PASSWORD", "secret")
	t.Setenv("REDIS_DB", "3")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "localhost:6380", cfg.RedisAddr)
	assert.Equal(t, "secret", cfg.RedisPassword)
	assert.Equal(t, 3, cfg.RedisDB)
}

func TestLoad_MissingDSN(t *testing.T) {
	t.Setenv("DB_DSN", "")

	_, err := Load()
	require.Error(t, err)
}

func TestLoad_MissingJWTSecret(t *testing.T) {
	t.Setenv("DB_DSN", "postgres://localhost/test")
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-telegram-token")
	t.Setenv("JWT_SECRET_KEY", "")

	_, err := Load()
	require.Error(t, err)
}

func TestLoad_MissingTelegramToken(t *testing.T) {
	t.Setenv("DB_DSN", "postgres://localhost/test")
	t.Setenv("JWT_SECRET_KEY", "test-jwt-secret")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")

	_, err := Load()
	require.Error(t, err)
}

func TestLoad_MaxUploadSizeDefault(t *testing.T) {
	setRequiredSecrets(t)
	t.Setenv("DB_DSN", "postgres://localhost/test")
	t.Setenv("MAX_UPLOAD_SIZE", "")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, int64(100<<20), cfg.MaxUploadSize)
}

func TestLoad_MaxUploadSizeOverride(t *testing.T) {
	setRequiredSecrets(t)
	t.Setenv("DB_DSN", "postgres://localhost/test")
	t.Setenv("MAX_UPLOAD_SIZE", "52428800") // 50 MB

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, int64(52428800), cfg.MaxUploadSize)
}

func TestLoad_MailAndAuthDefaults(t *testing.T) {
	setRequiredSecrets(t)
	t.Setenv("DB_DSN", "postgres://localhost/test")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 10*time.Second, cfg.MailSendTimeout)
	assert.Equal(t, 15*time.Minute, cfg.EmailCodeTTL)
	assert.Equal(t, 5, cfg.EmailCodeMaxAttempts)
	assert.Equal(t, 60*time.Second, cfg.EmailResendCooldown)
	assert.Equal(t, 5, cfg.EmailSendQuotaPerHour)
	assert.Equal(t, 10, cfg.LoginFailLimit)
	assert.Equal(t, 15*time.Minute, cfg.LoginFailWindow)
}

func TestLoad_MailSMTPEmptyOKInLocalDev(t *testing.T) {
	setRequiredSecrets(t) // APP_ENV=local
	t.Setenv("DB_DSN", "postgres://localhost/test")
	t.Setenv("MAIL_SMTP_HOST", "")
	t.Setenv("MAIL_SMTP_PORT", "")
	t.Setenv("MAIL_SMTP_USER", "")
	t.Setenv("MAIL_SMTP_PASSWORD", "")
	t.Setenv("MAIL_FROM", "")

	_, err := Load()
	require.NoError(t, err)
}

func TestLoad_MailSMTPRequiredOutsideLocalDev(t *testing.T) {
	t.Setenv("JWT_SECRET_KEY", "test-jwt-secret")
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-telegram-token")
	t.Setenv("DB_DSN", "postgres://localhost/test")
	t.Setenv("APP_ENV", "production")
	t.Setenv("MAIL_SMTP_HOST", "")
	t.Setenv("MAIL_SMTP_PORT", "")
	t.Setenv("MAIL_SMTP_USER", "")
	t.Setenv("MAIL_SMTP_PASSWORD", "")
	t.Setenv("MAIL_FROM", "")

	_, err := Load()
	require.Error(t, err)
}

func TestLoad_MailSMTPCompleteOutsideLocalDev(t *testing.T) {
	t.Setenv("JWT_SECRET_KEY", "test-jwt-secret")
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-telegram-token")
	t.Setenv("DB_DSN", "postgres://localhost/test")
	t.Setenv("APP_ENV", "production")
	t.Setenv("MAIL_SMTP_HOST", "smtp.example.com")
	t.Setenv("MAIL_SMTP_PORT", "587")
	t.Setenv("MAIL_SMTP_USER", "user")
	t.Setenv("MAIL_SMTP_PASSWORD", "pass")
	t.Setenv("MAIL_FROM", "noreply@example.com")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "smtp.example.com", cfg.MailSMTPHost)
}

// MAIL_SMTP_PORT is defaulted rather than required: 587 (submission +
// STARTTLS) is what every relay we'd plausibly use speaks, and the mailer
// only speaks STARTTLS anyway.
func TestLoad_MailSMTPPortDefaultsTo587(t *testing.T) {
	t.Setenv("JWT_SECRET_KEY", "test-jwt-secret")
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-telegram-token")
	t.Setenv("DB_DSN", "postgres://localhost/test")
	t.Setenv("APP_ENV", "production")
	t.Setenv("MAIL_SMTP_HOST", "smtp.gmail.com")
	t.Setenv("MAIL_SMTP_PORT", "")
	t.Setenv("MAIL_SMTP_USER", "user")
	t.Setenv("MAIL_SMTP_PASSWORD", "pass")
	t.Setenv("MAIL_FROM", "noreply@example.com")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "587", cfg.MailSMTPPort)
}

func TestLoadAdminSessionDefaults(t *testing.T) {
	t.Setenv("DB_DSN", "postgres://u:p@localhost:5432/db?sslmode=disable")
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	t.Setenv("JWT_SECRET_KEY", "test-secret")
	t.Setenv("APP_ENV", "local")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, 8*time.Hour, cfg.AdminSessionTTL)
	require.Equal(t, 24*time.Hour, cfg.AdminSessionMaxTTL)
}

func TestLoadAdminSessionOverride(t *testing.T) {
	t.Setenv("DB_DSN", "postgres://u:p@localhost:5432/db?sslmode=disable")
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	t.Setenv("JWT_SECRET_KEY", "test-secret")
	t.Setenv("APP_ENV", "local")
	t.Setenv("ADMIN_SESSION_TTL", "30m")
	t.Setenv("ADMIN_SESSION_MAX_TTL", "4h")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, 30*time.Minute, cfg.AdminSessionTTL)
	require.Equal(t, 4*time.Hour, cfg.AdminSessionMaxTTL)
}
