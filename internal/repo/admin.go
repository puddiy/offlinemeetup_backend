package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/uptrace/bun"
)

// Сентинелы репозитория. Пакет repo НЕ импортирует service (иначе получим
// цикл и размытую границу слоёв), поэтому сервис транслирует их в свои
// ErrNotFound/ErrAlreadyExists через errors.Is на своей границе.
var (
	ErrAdminNotFound   = errors.New("admin user not found")
	ErrAdminEmailTaken = errors.New("admin email already taken")
)

type AdminRepo struct {
	db *bun.DB
}

func NewAdminRepo(db *bun.DB) *AdminRepo {
	return &AdminRepo{db: db}
}

// normalizeAdminEmail приводит email к той же форме, по которой построен
// uq_admin_users_email_lower. Любой поиск и любая вставка обязаны проходить
// через неё, иначе "Admin@x.io" и "admin@x.io" разъедутся между Go и индексом.
func normalizeAdminEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// GetByEmail ищет админа по регистронезависимому email.
// Отсутствие строки — ErrAdminNotFound, а не (nil, nil): вызывающий здесь
// всегда обязан различить «нет такого» и «не смогли прочитать».
func (r *AdminRepo) GetByEmail(ctx context.Context, email string) (*domain.AdminUser, error) {
	admin := new(domain.AdminUser)
	err := r.db.NewSelect().
		Model(admin).
		Where("lower(email) = ?", normalizeAdminEmail(email)).
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAdminNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("select admin by email: %w", err)
	}
	return admin, nil
}

// GetByID перечитывает учётку по id. Вызывается на КАЖДОМ админском запросе
// (см. AdminAuthService.Authenticate), чтобы деактивация админа вступала в
// силу сразу, а не по истечении TTL сессии — ровно тот же приём, что
// UserStatusChecker.IsActive для мобильных пользователей.
func (r *AdminRepo) GetByID(ctx context.Context, id int64) (*domain.AdminUser, error) {
	admin := new(domain.AdminUser)
	err := r.db.NewSelect().
		Model(admin).
		Where("id = ?", id).
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAdminNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("select admin by id: %w", err)
	}
	return admin, nil
}

// Create вставляет нового админа. Email нормализуется здесь же, а гонку
// «два создания одного email» ловит не предварительный SELECT, а сам
// уникальный индекс — проверка-перед-вставкой была бы TOCTOU.
func (r *AdminRepo) Create(ctx context.Context, a *domain.AdminUser) error {
	a.Email = normalizeAdminEmail(a.Email)

	_, err := r.db.NewInsert().Model(a).Exec(ctx)
	if err != nil {
		if _, ok := uniqueViolation(err); ok {
			return ErrAdminEmailTaken
		}
		return fmt.Errorf("insert admin: %w", err)
	}
	return nil
}
