package middleware

import (
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// rateLimitScript атомарно инкрементирует счётчик окна и на первом хите ставит
// ему TTL, возвращая текущее значение. Классический fixed-window лимитер; INCR и
// PEXPIRE в одном скрипте исключают гонку «ключ без TTL».
var rateLimitScript = redis.NewScript(`
local c = redis.call("INCR", KEYS[1])
if c == 1 then
	redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
return c
`)

// RateLimiter ограничивает число запросов с одного IP до limit за окно window,
// считая в Redis — счётчик общий для всех инстансов. scope разносит счётчики
// разных групп эндпоинтов по разным ключам. Лимитер best-effort: ошибка Redis
// не блокирует запрос (fail-open), чтобы сбой кеша не положил аутентификацию.
func RateLimiter(rdb *redis.Client, log *slog.Logger, scope string, limit int64, window time.Duration, trustProxy bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := "ratelimit:" + scope + ":" + clientIP(r, trustProxy)
			enforceLimit(w, r, next, rdb, log, key, limit, window)
		})
	}
}

// UserRateLimiter — то же, что RateLimiter, но счётчик привязан к
// аутентифицированному пользователю, а не к IP. Нужен там, где злоупотребляет
// аккаунт, а не сеть: за NAT мобильного оператора один IP делят тысячи людей.
//
// Ставится ПОСЛЕ AuthMiddleware. Без пользователя в контексте отвечает 401 —
// неверный порядок middleware даёт закрытую дверь, а не общий бакет на всех.
func UserRateLimiter(rdb *redis.Client, log *slog.Logger, scope string, limit int64, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID, ok := GetUserIDFromContext(r.Context())
			if !ok {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			key := "ratelimit:" + scope + ":user:" + strconv.FormatInt(userID, 10)
			enforceLimit(w, r, next, rdb, log, key, limit, window)
		})
	}
}

// enforceLimit — общий fixed-window счётчик обоих лимитеров.
func enforceLimit(w http.ResponseWriter, r *http.Request, next http.Handler, rdb *redis.Client, log *slog.Logger, key string, limit int64, window time.Duration) {
	count, err := rateLimitScript.Run(r.Context(), rdb, []string{key}, window.Milliseconds()).Int64()
	if err != nil {
		log.Warn("rate limiter: redis error, allowing request", slog.Any("err", err))
		next.ServeHTTP(w, r)
		return
	}
	if count > limit {
		w.Header().Set("Retry-After", strconv.Itoa(int(window.Seconds())))
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	next.ServeHTTP(w, r)
}

// clientIP извлекает IP клиента для ключа rate-limit. Заголовкам прокси
// (X-Real-IP / X-Forwarded-For) доверяем ТОЛЬКО когда trustProxy=true — их может
// подделать любой клиент, а RemoteAddr подделать нельзя. За доверенным прокси,
// который сам перезаписывает эти заголовки, они надёжны; при прямом подключении
// (trustProxy=false) всегда используем RemoteAddr.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xrip := strings.TrimSpace(r.Header.Get("X-Real-IP")); xrip != "" {
			return xrip
		}
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			return strings.TrimSpace(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
