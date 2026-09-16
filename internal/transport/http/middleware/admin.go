package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
)

// AdminKey — ключ, под которым в контексте лежит *domain.AdminUser.
// Отдельный от UserIDKey намеренно: мобильный пользователь и администратор —
// разные сущности из разных таблиц, и перепутать их в хендлере не должно
// быть возможно даже случайно.
const AdminKey contextKey = "admin_user"

// AdminSessionCookieName — имя cookie с токеном сессии админки.
const AdminSessionCookieName = "admin_session"

// GetAdminFromContext достаёт администратора, положенного AdminSession.
func GetAdminFromContext(ctx context.Context) (*domain.AdminUser, bool) {
	admin, ok := ctx.Value(AdminKey).(*domain.AdminUser)
	return admin, ok
}

// AdminAuthenticator — то, что middleware требует от сервиса авторизации.
type AdminAuthenticator interface {
	Authenticate(ctx context.Context, token string) (*domain.AdminUser, error)
}

// AdminSession пускает дальше только запросы с валидной сессионной cookie и
// кладёт администратора в контекст.
//
// При отказе — редирект 303 на loginPath, а не 401: это браузерный UI, и
// пользователь должен увидеть форму входа, а не голый текст ошибки. Причина
// отказа наружу не раскрывается (нет сессии / истекла / учётка выключена —
// один и тот же редирект).
func AdminSession(auth AdminAuthenticator, loginPath string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(AdminSessionCookieName)
			if err != nil || cookie.Value == "" {
				http.Redirect(w, r, loginPath, http.StatusSeeOther)
				return
			}

			admin, err := auth.Authenticate(r.Context(), cookie.Value)
			if err != nil || admin == nil {
				http.Redirect(w, r, loginPath, http.StatusSeeOther)
				return
			}

			ctx := context.WithValue(r.Context(), AdminKey, admin)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAdminRole пропускает дальше только перечисленные роли.
// Ставится ПОСЛЕ AdminSession; отсутствие администратора в контексте
// трактуется как отказ, а не как паника — так неверный порядок middleware
// даёт закрытую дверь, а не открытую.
func RequireAdminRole(roles ...domain.AdminRole) func(http.Handler) http.Handler {
	allowed := make(map[domain.AdminRole]struct{}, len(roles))
	for _, role := range roles {
		allowed[role] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			admin, ok := GetAdminFromContext(r.Context())
			if !ok || admin == nil {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if _, ok := allowed[admin.Role]; !ok {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireSameOrigin — защита от CSRF для формоизменяющих запросов.
//
// Основная защита — SameSite=Strict на сессионной cookie (её ставит
// транспорт админки): браузер просто не пришлёт её с чужого сайта. Эта
// проверка — второй рубеж на случай браузера, который SameSite не уважает.
// Токен синхронизации не заводим: он дал бы третий рубеж ценой плюс-одного
// скрытого поля в каждой форме и состояния на сервере.
//
// Запрос без Origin на небезопасном методе отклоняется: любой актуальный
// браузер шлёт Origin на form-POST, так что его отсутствие — это не
// «старый браузер», а запрос не из формы.
func RequireSameOrigin(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			origin := r.Header.Get("Origin")
			if origin == "" {
				log.Info("admin request rejected: missing Origin", slog.String("path", r.URL.Path))
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}

			parsed, err := url.Parse(origin)
			if err != nil || parsed.Host != r.Host {
				log.Info("admin request rejected: cross-origin",
					slog.String("origin", origin),
					slog.String("host", r.Host),
					slog.String("path", r.URL.Path))
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
