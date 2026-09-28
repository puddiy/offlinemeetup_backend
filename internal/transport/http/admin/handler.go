package admin

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/config"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
)

// AdminAuthService — то, что транспорт админки требует от сервиса авторизации.
type AdminAuthService interface {
	Login(ctx context.Context, email, password string) (string, *domain.AdminUser, error)
	Authenticate(ctx context.Context, token string) (*domain.AdminUser, error)
	Logout(ctx context.Context, token string) error
}

// AdminTagSvc — справочник тегов (*service.AdminTagService).
type AdminTagSvc interface {
	List(ctx context.Context) ([]dto.AdminTagRow, error)
	Create(ctx context.Context, actorID int64, name, ip string) error
	Rename(ctx context.Context, actorID, tagID int64, name, ip string) error
	SetHidden(ctx context.Context, actorID, tagID int64, hidden bool, ip string) error
}

// AdminMeetupSvc — митапы в админке (*service.AdminMeetupService).
type AdminMeetupSvc interface {
	List(ctx context.Context, f service.AdminMeetupFilter) (dto.Page[dto.AdminMeetupRow], error)
	Get(ctx context.Context, id int64) (*dto.AdminMeetupDetail, error)
	CreateOfficial(ctx context.Context, actorID int64, req dto.CreateMeetupRequest, ip string) (int64, error)
	UpdateOfficial(ctx context.Context, actorID, meetupID int64, req dto.UpdateMeetupRequest, ip string) error
}

// AddressSuggester — подсказки адреса (*service.GeoService, DaData).
type AddressSuggester interface {
	SuggestAddress(ctx context.Context, query string) ([]dto.AddressSuggestion, error)
}

// Deps — зависимости транспорта админки. Структура, а не позиционные
// аргументы: их больше десятка, и два интерфейса одной формы легко
// перепутать местами (тот же довод, что у service.ModerationDeps).
// Незаданное поле — nil: тесты передают только то, что проверяют.
type Deps struct {
	Auth       AdminAuthService
	Users      AdminUserSvc
	Moderation ModerationSvc
	Meetups    AdminMeetupSvc
	Tags       AdminTagSvc
	Geo        AddressSuggester
	Audit      service.AuditRecorder
	Render     *Renderer
	WS         Broadcaster
	Cfg        *config.Config
	Log        *slog.Logger
}

type Handler struct {
	auth       AdminAuthService
	users      AdminUserSvc
	moderation ModerationSvc
	meetups    AdminMeetupSvc
	tags       AdminTagSvc
	geo        AddressSuggester
	audit      service.AuditRecorder
	render     *Renderer
	ws         Broadcaster
	cfg        *config.Config
	log        *slog.Logger
}

func NewHandler(d Deps) *Handler {
	return &Handler{
		auth:       d.Auth,
		users:      d.Users,
		moderation: d.Moderation,
		meetups:    d.Meetups,
		tags:       d.Tags,
		geo:        d.Geo,
		audit:      d.Audit,
		render:     d.Render,
		ws:         d.WS,
		cfg:        d.Cfg,
		log:        d.Log,
	}
}

// secureCookies — ставить ли флаг Secure. В local его нельзя ставить
// безусловно: браузер не отдаст Secure-cookie обратно по http://localhost,
// и вход будет молча не работать. Везде, кроме local, флаг обязателен.
func (h *Handler) secureCookies() bool {
	return h.cfg.Env != "local"
}

// setSessionCookie кладёт токен сессии в браузер.
//
// Path=/admin — cookie не уедет в мобильное API. SameSite=Strict — основная
// защита от CSRF (RequireSameOrigin — второй рубеж). HttpOnly — XSS в панели
// не должен превращаться в кражу сессии. MaxAge НЕ выставляем: сессионная
// cookie умирает вместе с окном браузера, а сервер и так держит своё TTL.
func (h *Handler) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     middleware.AdminSessionCookieName,
		Value:    token,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   h.secureCookies(),
		SameSite: http.SameSiteStrictMode,
	})
}

// clearSessionCookie гасит cookie на выходе.
func (h *Handler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     middleware.AdminSessionCookieName,
		Value:    "",
		Path:     "/admin",
		HttpOnly: true,
		Secure:   h.secureCookies(),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// clientIP — IP для журнала. Заголовки прокси учитываются только при
// TRUST_PROXY_HEADERS=true, ровно как в RateLimiter: иначе в аудит поедет
// то, что клиент сам себе написал, и журнал станет бесполезен именно там,
// где он нужнее всего.
func (h *Handler) clientIP(r *http.Request) string {
	if h.cfg.TrustProxyHeaders {
		if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
		// Цепочка "client, proxy1, proxy2": клиент — первый элемент. Логика
		// та же, что в RateLimiter, чтобы аудит и лимит видели один и тот же IP.
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			first, _, _ := strings.Cut(fwd, ",")
			return strings.TrimSpace(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// recordAudit пишет событие и НЕ проваливает запрос при ошибке записи.
//
// Осознанный компромисс на этом милстоуне: вход и выход уже состоялись к
// моменту записи, откатывать нечего, и ронять сессию из-за недоступного
// журнала хуже, чем потерять строку (потеря видна в логах как Error).
// Для НЕОБРАТИМЫХ действий милстоунов B–E правило другое: там мутация и
// запись журнала идут одной транзакцией через AuditRepo.Record(ctx, tx, ...).
func (h *Handler) recordAudit(r *http.Request, ev service.AuditEvent) {
	if err := h.audit.Record(r.Context(), nil, ev); err != nil {
		h.log.Error("recording admin audit entry",
			slog.String("action", ev.Action),
			slog.Int64("admin_id", ev.AdminID),
			slog.Any("error", err))
	}
}
