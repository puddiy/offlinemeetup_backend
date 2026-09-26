package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func userRequest(userID int64, ip string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/reports", nil)
	req.RemoteAddr = ip + ":5555"
	if userID != 0 {
		req = req.WithContext(context.WithValue(req.Context(), UserIDKey, userID))
	}
	return req
}

func serveLimited(h http.Handler, req *http.Request) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// Главное свойство: счётчик привязан к ПОЛЬЗОВАТЕЛЮ. За NAT мобильного
// оператора один IP делят тысячи людей — лимит по IP блокировал бы соседей
// спамера и не мешал бы самому спамеру, сменившему сеть.
func TestUserRateLimiterCountsPerUserNotPerIP(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := UserRateLimiter(rdb, slog.New(slog.DiscardHandler), "reports", 2, time.Hour)(ok)

	require.Equal(t, http.StatusOK, serveLimited(h, userRequest(1, "10.0.0.1")))
	require.Equal(t, http.StatusOK, serveLimited(h, userRequest(1, "10.0.0.1")))

	// Тот же пользователь из другой сети — всё равно отказ.
	require.Equal(t, http.StatusTooManyRequests, serveLimited(h, userRequest(1, "192.168.1.1")))

	// Другой пользователь за тем же NAT — пропускаем.
	require.Equal(t, http.StatusOK, serveLimited(h, userRequest(2, "10.0.0.1")))
}

// Без пользователя в контексте — закрытая дверь, а не общий бакет «0» на всех
// анонимов. Такое бывает только при неверном порядке middleware.
func TestUserRateLimiterWithoutUserIsUnauthorized(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true })
	h := UserRateLimiter(rdb, slog.New(slog.DiscardHandler), "reports", 2, time.Hour)(next)

	require.Equal(t, http.StatusUnauthorized, serveLimited(h, userRequest(0, "10.0.0.1")))
	require.False(t, reached)
}

// Как и лимит по IP: сбой Redis не должен класть эндпоинт.
func TestUserRateLimiterFailsOpenOnRedisError(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()
	mr.Close()

	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := UserRateLimiter(rdb, slog.New(slog.DiscardHandler), "reports", 1, time.Hour)(ok)

	require.Equal(t, http.StatusOK, serveLimited(h, userRequest(1, "10.0.0.1")))
}
