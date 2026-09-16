package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/cache"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/config"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo"
	"golang.org/x/crypto/bcrypt"
)

// AdminRepository — то, что AdminAuthService требует от хранилища админов.
// Интерфейс объявлен здесь, на стороне потребителя: это и шов для моков, и
// причина, по которой repo не знает про service.
type AdminRepository interface {
	GetByEmail(ctx context.Context, email string) (*domain.AdminUser, error)
	GetByID(ctx context.Context, id int64) (*domain.AdminUser, error)
}

// AdminSessionStore — то, что сервис требует от хранилища сессий.
type AdminSessionStore interface {
	Save(ctx context.Context, tokenHash string, sess cache.AdminSession, ttl time.Duration) error
	Get(ctx context.Context, tokenHash string) (cache.AdminSession, bool, error)
	Touch(ctx context.Context, tokenHash string, ttl time.Duration) error
	Delete(ctx context.Context, tokenHash string) error
}

// dummyAdminHash — фиксированный bcrypt-хэш, с которым сравнивается пароль,
// когда админа с таким email нет. Тот же приём, что dummyPasswordHash в
// auth_login.go: без него ответ на несуществующий email приходил бы заметно
// быстрее (bcrypt не запускался), и время ответа становилось оракулом
// существования учётки.
var dummyAdminHash = func() []byte {
	h, err := bcrypt.GenerateFromPassword([]byte("dummy-password-for-admin-timing-parity"), bcryptCost)
	if err != nil {
		panic("service: generating dummy admin bcrypt hash: " + err.Error())
	}
	return h
}()

type AdminAuthService struct {
	repo     AdminRepository
	sessions AdminSessionStore
	cfg      *config.Config
	log      *slog.Logger
}

func NewAdminAuthService(r AdminRepository, sessions AdminSessionStore, cfg *config.Config, log *slog.Logger) *AdminAuthService {
	return &AdminAuthService{repo: r, sessions: sessions, cfg: cfg, log: log}
}

// hashSessionToken хэширует значение cookie перед тем, как оно станет ключом
// Redis. Токен — вывод CSPRNG, а не пароль, поэтому быстрого SHA-256
// достаточно (ни соли, ни bcrypt не нужно) — ровно как у refresh-токенов.
func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// newSessionToken генерирует 32 байта из CSPRNG.
func newSessionToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate admin session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Login проверяет пару email/пароль и заводит сессию. Возвращает сырой токен
// (его увидит только браузер, в виде cookie) и учётку.
//
// Все отказы — неизвестный email, неверный пароль, деактивированная учётка —
// возвращают ОДИН сентинел ErrUnauthorized. Различать их в ответе значит
// раздавать информацию о том, какие админские учётки существуют.
func (s *AdminAuthService) Login(ctx context.Context, email, password string) (string, *domain.AdminUser, error) {
	admin, err := s.repo.GetByEmail(ctx, email)
	if err != nil && !errors.Is(err, repo.ErrAdminNotFound) {
		return "", nil, fmt.Errorf("admin login: %w", err)
	}

	// Ветка «нет такого админа» всё равно платит цену bcrypt (см.
	// dummyAdminHash), поэтому дальше идём с одинаковой стоимостью.
	hash := dummyAdminHash
	if admin != nil {
		hash = []byte(admin.PasswordHash)
	}
	if cmpErr := bcrypt.CompareHashAndPassword(hash, []byte(password)); cmpErr != nil {
		return "", nil, ErrUnauthorized
	}
	if admin == nil || !admin.IsActive {
		return "", nil, ErrUnauthorized
	}

	token, err := newSessionToken()
	if err != nil {
		return "", nil, err
	}

	sess := cache.AdminSession{
		AdminID:  admin.ID,
		Role:     string(admin.Role),
		IssuedAt: time.Now().UTC(),
	}
	if err := s.sessions.Save(ctx, hashSessionToken(token), sess, s.cfg.AdminSessionTTL); err != nil {
		return "", nil, fmt.Errorf("admin login: %w", err)
	}

	return token, admin, nil
}

// Authenticate проверяет токен из cookie и возвращает СВЕЖУЮ учётку из БД.
//
// Перечитывание строки на каждом запросе — намеренная цена: так снятие
// IsActive или понижение роли действуют немедленно, а не через 8 часов, когда
// истечёт сессия. Тот же приём, что UserStatusChecker.IsActive для мобильных
// пользователей (internal/transport/http/middleware/auth.go).
//
// Успешная проверка продлевает окно простоя, но НЕ трогает IssuedAt: потолок
// AdminSessionMaxTTL считается от входа и продлением не двигается.
func (s *AdminAuthService) Authenticate(ctx context.Context, token string) (*domain.AdminUser, error) {
	if token == "" {
		return nil, ErrUnauthorized
	}

	tokenHash := hashSessionToken(token)
	sess, found, err := s.sessions.Get(ctx, tokenHash)
	if err != nil {
		return nil, fmt.Errorf("admin authenticate: %w", err)
	}
	if !found {
		return nil, ErrUnauthorized
	}

	if time.Since(sess.IssuedAt) > s.cfg.AdminSessionMaxTTL {
		// Чистим ключ, чтобы протухшая по потолку сессия не занимала место
		// до конца своего TTL простоя.
		if delErr := s.sessions.Delete(ctx, tokenHash); delErr != nil {
			s.log.Error("dropping expired admin session", slog.Any("error", delErr))
		}
		return nil, ErrUnauthorized
	}

	admin, err := s.repo.GetByID(ctx, sess.AdminID)
	if errors.Is(err, repo.ErrAdminNotFound) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, fmt.Errorf("admin authenticate: %w", err)
	}
	if !admin.IsActive {
		return nil, ErrUnauthorized
	}

	if err := s.sessions.Touch(ctx, tokenHash, s.cfg.AdminSessionTTL); err != nil {
		// Не проваливаем запрос: сессия валидна, просто не продлилась.
		s.log.Error("touching admin session", slog.Any("error", err))
	}

	return admin, nil
}

// Logout удаляет сессию. Неизвестный токен — не ошибка: повторный выход и
// выход по протухшей cookie обязаны выглядеть одинаково успешно.
func (s *AdminAuthService) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.sessions.Delete(ctx, hashSessionToken(token))
}
