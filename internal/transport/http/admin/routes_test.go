package admin

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-chi/chi/v5"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/config"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newTestRoutes(t *testing.T) http.Handler {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	log := slog.New(slog.DiscardHandler)
	rend, err := NewRenderer(log)
	require.NoError(t, err)
	cfg := &config.Config{Env: "local"}
	h := NewHandler(&stubAuth{loginErr: service.ErrUnauthorized}, &recordingAudit{}, rend, cfg, log)

	root := chi.NewRouter()
	root.Mount("/admin", Routes(h, rdb, log, cfg))
	return root
}

func serve(root http.Handler, req *http.Request) *httptest.ResponseRecorder {
	req.RemoteAddr = "7.7.7.7:5555"
	rec := httptest.NewRecorder()
	root.ServeHTTP(rec, req)
	return rec
}

func loginPost(origin string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "http://example.com/admin/login",
		strings.NewReader(url.Values{"email": {"a@x.io"}, "password": {"pw"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", origin)
	return req
}

func TestRoutesBasics(t *testing.T) {
	root := newTestRoutes(t)

	rec := serve(root, httptest.NewRequest(http.MethodGet, "/admin/", nil))
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/admin/login", rec.Header().Get("Location"))

	rec = serve(root, httptest.NewRequest(http.MethodGet, "/admin/login", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	rec = serve(root, httptest.NewRequest(http.MethodGet, "/admin/static/htmx.min.js", nil))
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestRoutesLoginGetNotThrottled(t *testing.T) {
	root := newTestRoutes(t)
	for i := 0; i < 15; i++ {
		rec := serve(root, httptest.NewRequest(http.MethodGet, "/admin/login", nil))
		require.Equal(t, http.StatusOK, rec.Code, "GET #%d", i+1)
	}
}

func TestRoutesLoginPostThrottled(t *testing.T) {
	root := newTestRoutes(t)
	for i := 0; i < 10; i++ {
		rec := serve(root, loginPost("http://example.com"))
		require.Equal(t, http.StatusUnauthorized, rec.Code, "POST #%d", i+1)
	}
	rec := serve(root, loginPost("http://example.com"))
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
}

func TestRoutesLoginPostCrossOriginForbidden(t *testing.T) {
	root := newTestRoutes(t)
	rec := serve(root, loginPost("http://evil.example"))
	require.Equal(t, http.StatusForbidden, rec.Code)
}
