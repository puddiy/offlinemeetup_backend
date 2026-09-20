package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/config"
	"github.com/stretchr/testify/require"
)

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		trust      bool
		remoteAddr string
		realIP     string
		xff        string
		want       string
	}{
		{"untrusted ignores headers", false, "9.9.9.9:1234", "5.5.5.5", "1.2.3.4", "9.9.9.9"},
		{"trusted X-Real-IP wins", true, "9.9.9.9:1234", "5.5.5.5", "1.2.3.4", "5.5.5.5"},
		{"trusted XFF takes first element", true, "9.9.9.9:1234", "", "1.2.3.4, 10.0.0.1", "1.2.3.4"},
		{"trusted no headers falls back to RemoteAddr", true, "9.9.9.9:1234", "", "", "9.9.9.9"},
		{"RemoteAddr without port", false, "9.9.9.9", "", "", "9.9.9.9"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{cfg: &config.Config{TrustProxyHeaders: tc.trust}}
			req := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.realIP != "" {
				req.Header.Set("X-Real-IP", tc.realIP)
			}
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			require.Equal(t, tc.want, h.clientIP(req))
		})
	}
}
