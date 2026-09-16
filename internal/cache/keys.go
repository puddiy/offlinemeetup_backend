package cache

import (
	"strconv"
	"strings"
)

// Префиксы ключей — единственный источник правды по форматам.
const (
	userChatsPrefix     = "user_chats:"
	tagsAllKey          = "tags:all"
	profilePrefix       = "profile:"
	meetupPrefix        = "meetup:"
	presenceConnsPrefix = "presence:conns:"
	presenceSeenPrefix  = "presence:lastseen:"

	pendingRegPrefix   = "auth:pending_reg:"
	pendingResetPrefix = "auth:pending_reset:"
	loginFailPrefix    = "auth:login_fail:"
	mailCooldownPrefix = "auth:mail_cooldown:"
	mailQuotaPrefix    = "auth:mail_quota:"

	// Password-reset (forgot-password) flow gets its OWN cooldown/quota key
	// namespace, separate from mailCooldownPrefix/mailQuotaPrefix which are
	// shared by Register/ResendCode. Sharing one namespace between the two
	// flows was a cross-endpoint account-enumeration oracle: ForgotPassword
	// only claims the shared key on the found-account path, but ResendCode
	// reports a hit on that same key as a visible 429 — two calls (forgot-
	// password then resend-code on the same email) would then deterministically
	// reveal whether the account exists. See task-6 report, Critical #1.
	mailResetCooldownPrefix = "auth:mail_reset_cooldown:"
	mailResetQuotaPrefix    = "auth:mail_reset_quota:"

	// resetAttemptsPrefix backs an UNCONDITIONAL wrong-code counter for
	// ResetPassword, keyed by email regardless of whether a real
	// PendingReset object exists for it (same idea as loginFailPrefix — a
	// counter that doesn't require a real account to exist). Without this,
	// a nonexistent email always got 400 forever while a real account
	// eventually hit 429 on a predictable call count — a second
	// account-enumeration oracle, this time via forgot-password +
	// reset-password. See task-6 report, fix round 2.
	resetAttemptsPrefix = "auth:reset_attempts:"

	// adminSessionPrefix — сессии админ-панели. Ключ строится по SHA-256
	// от токена из cookie, а не по самому токену: дамп Redis тогда не даёт
	// готовых к использованию сессий (тот же приём, что у refresh_tokens,
	// где в БД лежит только хэш).
	adminSessionPrefix = "admin:session:"
)

// UserChatsKey возвращает ключ со списком чатов пользователя.
func UserChatsKey(userID int64) string {
	return userChatsPrefix + strconv.FormatInt(userID, 10)
}

// TagsKey возвращает ключ глобального списка тегов (теги общие для всех).
func TagsKey() string {
	return tagsAllKey
}

// ProfileKey возвращает ключ профиля пользователя (одинаков для всех смотрящих).
func ProfileKey(userID int64) string {
	return profilePrefix + strconv.FormatInt(userID, 10)
}

// MeetupKey возвращает ключ инвариантного снапшота митапа (IsMember накладывается отдельно).
func MeetupKey(meetupID int64) string {
	return meetupPrefix + strconv.FormatInt(meetupID, 10)
}

// PresenceConnsKey — ключ Redis-множества активных WS-соединений пользователя
// (по одному connID на соединение). Пользователь online, пока SCARD > 0.
func PresenceConnsKey(userID int64) string {
	return presenceConnsPrefix + strconv.FormatInt(userID, 10)
}

// PresenceLastSeenKey — ключ с unix-таймстампом последнего ухода в offline.
func PresenceLastSeenKey(userID int64) string {
	return presenceSeenPrefix + strconv.FormatInt(userID, 10)
}

// PendingRegKey — ключ незавершённой регистрации (ADR-8): {password_hash,
// code_hash, attempts, username}, TTL 15 мин, единственный источник правды до
// verify. Email приводится к lower — ADR-3 хранит/ищет email по lower+trim.
//
// В ключ входит ещё и regID — случайный идентификатор попытки регистрации,
// который /register возвращает клиенту, а /verify-email обязан прислать
// обратно. Без него ключ был бы один на email, и параллельный /register на
// чужой email ПЕРЕЗАПИСЫВАЛ бы живой pending-объект: жертва получала два
// письма с кодами, и, введя код из последнего, создавала аккаунт с паролем
// атакующего (а на ADR-7-ветке — прикручивала его пароль к уже
// существующему аккаунту). С regID попытки живут параллельно и не видят
// друг друга, поэтому перезаписать чужую невозможно в принципе.
//
// Пара (email, regID) целиком лежит в ключе, так что «не тот email» и «не
// тот regID» — это просто промах по ключу, неотличимый от истёкшего TTL. Ни
// сверки полей, ни второго источника правды.
func PendingRegKey(email, regID string) string {
	return pendingRegPrefix + strings.ToLower(strings.TrimSpace(email)) + ":" + regID
}

// PendingResetKey — ключ незавершённого сброса пароля (forgot/reset), та же
// форма, что PendingRegKey, но отдельное пространство ключей.
func PendingResetKey(email string) string {
	return pendingResetPrefix + strings.ToLower(strings.TrimSpace(email))
}

// LoginFailKey — ключ счётчика неудачных попыток входа (ADR-13). login — то,
// что ввёл вызывающий (email или username, ADR-2); приводим к lower здесь,
// чтобы "Denis" и "denis" делили один счётчик.
func LoginFailKey(login string) string {
	return loginFailPrefix + strings.ToLower(strings.TrimSpace(login))
}

// MailCooldownKey — ключ анти-даблклик кулдауна на повторную отправку письма.
func MailCooldownKey(email string) string {
	return mailCooldownPrefix + strings.ToLower(strings.TrimSpace(email))
}

// MailQuotaKey — ключ часовой квоты на отправку писем для одного email.
func MailQuotaKey(email string) string {
	return mailQuotaPrefix + strings.ToLower(strings.TrimSpace(email))
}

// MailResetCooldownKey — кулдаун-ключ анти-даблклика для forgot-password,
// в ОТДЕЛЬНОМ пространстве ключей от MailCooldownKey (см. комментарий у
// mailResetCooldownPrefix — общий namespace с ResendCode был oracle'ом
// существования аккаунта).
func MailResetCooldownKey(email string) string {
	return mailResetCooldownPrefix + strings.ToLower(strings.TrimSpace(email))
}

// MailResetQuotaKey — часовая квота на отправку писем сброса пароля, в
// ОТДЕЛЬНОМ пространстве ключей от MailQuotaKey (та же причина, что у
// MailResetCooldownKey).
func MailResetQuotaKey(email string) string {
	return mailResetQuotaPrefix + strings.ToLower(strings.TrimSpace(email))
}

// ResetAttemptsKey — ключ счётчика неверных кодов сброса пароля для email,
// СУЩЕСТВУЮЩИЙ НЕЗАВИСИМО от того, есть ли реальный PendingReset для этого
// email (см. комментарий у resetAttemptsPrefix).
func ResetAttemptsKey(email string) string {
	return resetAttemptsPrefix + strings.ToLower(strings.TrimSpace(email))
}

// AdminSessionKey — ключ сессии админ-панели. tokenHash — hex-строка SHA-256
// от значения cookie; сырой токен в Redis не попадает никогда.
func AdminSessionKey(tokenHash string) string {
	return adminSessionPrefix + tokenHash
}
