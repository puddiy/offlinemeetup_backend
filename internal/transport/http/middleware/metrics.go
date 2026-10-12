package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// MetricsGuard закрывает /metrics.
//
// Ручка смонтирована в корне публичного домена, и без защиты любой видел бы
// метрики кеша, почты и всё, что в реестр добавят потом. Правила:
//   - token задан — нужен заголовок `Authorization: Bearer <token>`;
//   - token пуст — ручка открыта ТОЛЬКО при APP_ENV=local, иначе 404.
//
// Пустой токен не роняет старт (в отличие от JWT_SECRET_KEY): метрики сейчас
// никто не собирает, и закрытая ручка лучше упавшего деплоя. Но и открытой
// по забывчивости она не остаётся — отсюда 404, а не «пропустить».
//
// Сравниваются SHA-256 от токенов, а не сами токены: subtle.ConstantTimeCompare
// на строках разной длины отвечает сразу и выдаёт длину секрета по времени.
func MetricsGuard(token, env string) func(http.Handler) http.Handler {
	want := sha256.Sum256([]byte(token))

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" {
				if env == "local" {
					next.ServeHTTP(w, r)
					return
				}
				http.NotFound(w, r)
				return
			}

			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			sum := sha256.Sum256([]byte(got))
			if !ok || subtle.ConstantTimeCompare(sum[:], want[:]) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
