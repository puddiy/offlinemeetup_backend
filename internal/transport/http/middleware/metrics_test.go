package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMetricsGuard(t *testing.T) {
	cases := []struct {
		name     string
		token    string
		env      string
		header   string
		wantCode int
	}{
		{"valid bearer", "s3cret", "prod", "Bearer s3cret", http.StatusOK},
		{"missing header", "s3cret", "prod", "", http.StatusUnauthorized},
		{"wrong token", "s3cret", "prod", "Bearer nope", http.StatusUnauthorized},
		{"prefix of token", "s3cret", "prod", "Bearer s3cre", http.StatusUnauthorized},
		{"basic scheme", "s3cret", "prod", "Basic s3cret", http.StatusUnauthorized},
		{"token required even locally", "s3cret", "local", "", http.StatusUnauthorized},
		{"no token locally is open", "", "local", "", http.StatusOK},
		{"no token in dev is closed", "", "dev", "", http.StatusNotFound},
		{"no token in prod", "", "prod", "", http.StatusNotFound},
		{"no token ignores bearer", "", "prod", "Bearer anything", http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			h := MetricsGuard(tc.token, tc.env)(okHandler(&reached))

			req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			require.Equal(t, tc.wantCode, rec.Code)
			require.Equal(t, tc.wantCode == http.StatusOK, reached)
			if tc.wantCode == http.StatusUnauthorized {
				require.Equal(t, `Bearer realm="metrics"`, rec.Header().Get("WWW-Authenticate"))
			}
		})
	}
}
