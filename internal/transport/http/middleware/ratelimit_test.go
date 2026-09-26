package middleware

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// TestClientIP pins the anti-spoofing contract: X-Real-IP / X-Forwarded-For are
// honored ONLY when trustProxy=true. A regression here is silent and total —
// trusting those headers unconditionally lets any client mint a fresh rate-limit
// bucket per request — so this is the cheapest, highest-value test to keep.
func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		trustProxy bool
		remoteAddr string
		xRealIP    string
		xff        string
		want       string
	}{
		{
			name:       "no trust ignores proxy headers, uses RemoteAddr host",
			trustProxy: false,
			remoteAddr: "1.2.3.4:5678",
			xRealIP:    "8.8.8.8",
			xff:        "9.9.9.9",
			want:       "1.2.3.4",
		},
		{
			name:       "trust prefers X-Real-IP",
			trustProxy: true,
			remoteAddr: "1.2.3.4:5678",
			xRealIP:    "8.8.8.8",
			xff:        "9.9.9.9, 10.0.0.1",
			want:       "8.8.8.8",
		},
		{
			name:       "trust falls back to first X-Forwarded-For hop",
			trustProxy: true,
			remoteAddr: "1.2.3.4:5678",
			xff:        "9.9.9.9, 10.0.0.1",
			want:       "9.9.9.9",
		},
		{
			name:       "trust but no proxy headers uses RemoteAddr host",
			trustProxy: true,
			remoteAddr: "1.2.3.4:5678",
			want:       "1.2.3.4",
		},
		{
			name:       "X-Real-IP is trimmed",
			trustProxy: true,
			remoteAddr: "1.2.3.4:5678",
			xRealIP:    "   8.8.8.8   ",
			want:       "8.8.8.8",
		},
		{
			name:       "whitespace-only X-Real-IP falls through to XFF",
			trustProxy: true,
			remoteAddr: "1.2.3.4:5678",
			xRealIP:    "   ",
			xff:        "9.9.9.9",
			want:       "9.9.9.9",
		},
		{
			name:       "RemoteAddr without port is returned verbatim",
			trustProxy: false,
			remoteAddr: "1.2.3.4",
			want:       "1.2.3.4",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remoteAddr
			if tc.xRealIP != "" {
				r.Header.Set("X-Real-IP", tc.xRealIP)
			}
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			require.Equal(t, tc.want, clientIP(r, tc.trustProxy))
		})
	}
}

func TestRateLimiter_BlocksOverLimit(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	handler := RateLimiter(rdb, slog.New(slog.DiscardHandler), "test", 2, time.Minute, false)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	)

	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "1.2.3.4:1111"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	require.Equal(t, http.StatusOK, call().Code, "1st within limit")
	require.Equal(t, http.StatusOK, call().Code, "2nd within limit")

	third := call()
	require.Equal(t, http.StatusTooManyRequests, third.Code, "3rd over limit")
	require.NotEmpty(t, third.Header().Get("Retry-After"), "429 must carry Retry-After")
}

func TestRateLimiter_FailsOpenOnRedisError(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()
	mr.Close() // Redis unreachable → the limiter must fail open, not block traffic.

	handler := RateLimiter(rdb, slog.New(slog.DiscardHandler), "test", 1, time.Minute, false)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "1.2.3.4:1111"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code, "a Redis failure must not block the request (fail-open)")
}
