package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/config"
	"github.com/stretchr/testify/assert"
)

const testSecret = "test-secret"

func makeToken(t *testing.T, secret string, userID int64, exp time.Time) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"userID": userID,
		"exp":    exp.Unix(),
	})
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}
	return s
}

// echoHandler пишет ID пользователя из контекста либо "anon", если его нет.
func echoHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if id, ok := GetUserIDFromContext(r.Context()); ok {
			_, _ = fmt.Fprintf(w, "%d", id)
			return
		}
		_, _ = fmt.Fprint(w, "anon")
	}
}

// stubChecker — управляемая заглушка UserStatusChecker.
type stubChecker struct {
	active bool
	err    error
}

func (s stubChecker) IsActive(_ context.Context, _ int64) (bool, error) {
	return s.active, s.err
}

func TestAuthMiddleware(t *testing.T) {
	cfg := &config.Config{JWTSecret: testSecret}
	// nil-чекер => проверка статуса пропускается (как и раньше).
	handler := AuthMiddleware(cfg, nil)(echoHandler())

	tests := []struct {
		name       string
		authHeader string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "valid token",
			authHeader: "Bearer " + makeToken(t, testSecret, 42, time.Now().Add(time.Hour)),
			wantStatus: http.StatusOK,
			wantBody:   "42",
		},
		{
			name:       "missing header",
			authHeader: "",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "empty bearer token",
			authHeader: "Bearer ",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "wrong signature",
			authHeader: "Bearer " + makeToken(t, "other-secret", 42, time.Now().Add(time.Hour)),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "expired token",
			authHeader: "Bearer " + makeToken(t, testSecret, 42, time.Now().Add(-time.Hour)),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "garbage token",
			authHeader: "Bearer not.a.jwt",
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
			if tt.wantBody != "" {
				assert.Equal(t, tt.wantBody, rec.Body.String())
			}
		})
	}
}

func TestAuthMiddleware_StatusCheck(t *testing.T) {
	cfg := &config.Config{JWTSecret: testSecret}
	token := "Bearer " + makeToken(t, testSecret, 42, time.Now().Add(time.Hour))

	tests := []struct {
		name       string
		checker    stubChecker
		wantStatus int
	}{
		{"active user passes", stubChecker{active: true}, http.StatusOK},
		{"banned user forbidden", stubChecker{active: false}, http.StatusForbidden},
		{"checker error -> 500", stubChecker{err: errors.New("db down")}, http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := AuthMiddleware(cfg, tt.checker)(echoHandler())
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", token)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)
			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

func TestUserIdentityMiddleware(t *testing.T) {
	cfg := &config.Config{JWTSecret: testSecret}
	handler := UserIdentityMiddleware(cfg)(echoHandler())

	tests := []struct {
		name       string
		authHeader string
		wantBody   string
	}{
		{
			name:       "valid token sets identity",
			authHeader: "Bearer " + makeToken(t, testSecret, 7, time.Now().Add(time.Hour)),
			wantBody:   "7",
		},
		{
			name:       "no header passes through anonymously",
			authHeader: "",
			wantBody:   "anon",
		},
		{
			name:       "invalid token passes through anonymously",
			authHeader: "Bearer " + makeToken(t, "other-secret", 7, time.Now().Add(time.Hour)),
			wantBody:   "anon",
		},
		{
			name:       "empty bearer passes through anonymously",
			authHeader: "Bearer ",
			wantBody:   "anon",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			// UserIdentityMiddleware всегда пропускает запрос дальше.
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tt.wantBody, rec.Body.String())
		})
	}
}
