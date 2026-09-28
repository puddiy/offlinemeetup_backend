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

var (
	ErrTagNotFound = errors.New("tag not found")
	// ErrTagNameTaken — имя занято с точностью до регистра (uq_tags_name_lower).
	ErrTagNameTaken = errors.New("tag name already taken")
	// ErrTagUnavailable — среди переданных id есть несуществующий тег или
	// скрытый, которого у этого митапа/профиля ещё нет. Оба случая для
	// клиента одно и то же: «такого тега выбрать нельзя».
	ErrTagUnavailable = errors.New("tag does not exist or is hidden")
)

// TagUsage — строка справочника тегов в админке: тег и сколько раз он
// используется. Счётчики помогают решить, скрывать ли тег.
type TagUsage struct {
	ID       int64  `bun:"id"`
	Name     string `bun:"name"`
	IsHidden bool   `bun:"is_hidden"`
	Meetups  int    `bun:"meetups"`
	Users    int    `bun:"users"`
}

type TagRepo struct {
	db *bun.DB
}

func NewTagRepo(db *bun.DB) *TagRepo {
	return &TagRepo{db: db}
}

// RunInTx выполняет fn в транзакции: мутация справочника и запись журнала
// идут вместе.
func (r *TagRepo) RunInTx(ctx context.Context, fn func(tx bun.Tx) error) error {
	return r.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return fn(tx)
	})
}

// ListVisible отдаёт каталог для мобильного клиента — без скрытых тегов.
func (r *TagRepo) ListVisible(ctx context.Context) ([]domain.Tag, error) {
	var tags []domain.Tag
	err := r.db.NewSelect().
		Model(&tags).
		Where("is_hidden = false").
		Order("name ASC").
		Scan(ctx)
	return tags, err
}

// ListWithUsage отдаёт ВСЕ теги со счётчиками использования: сначала
// видимые, потом скрытые, внутри — по алфавиту без учёта регистра.
func (r *TagRepo) ListWithUsage(ctx context.Context) ([]TagUsage, error) {
	var rows []TagUsage
	err := r.db.NewRaw(`SELECT t.id, t.name, t.is_hidden,
		(SELECT count(*) FROM meetup_tags mt WHERE mt.tag_id = t.id) AS meetups,
		(SELECT count(*) FROM user_tags ut WHERE ut.tag_id = t.id) AS users
		FROM tags t
		ORDER BY t.is_hidden, lower(t.name)`).Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("list tags with usage: %w", err)
	}
	return rows, nil
}

// CreateTx добавляет тег. Занятое имя ловит уникальный индекс, а не
// предварительный SELECT — проверка перед вставкой была бы TOCTOU.
func (r *TagRepo) CreateTx(ctx context.Context, tx bun.IDB, name string) (*domain.Tag, error) {
	tag := &domain.Tag{Name: name}
	if _, err := tx.NewInsert().Model(tag).Returning("id").Exec(ctx); err != nil {
		return nil, mapTagWriteError(err, "insert tag")
	}
	return tag, nil
}

// RenameTx переименовывает тег и возвращает прежнее имя — оно уходит в
// журнал. Строка блокируется FOR UPDATE, чтобы прежнее имя в журнале
// соответствовало тому, что реально переименовали.
func (r *TagRepo) RenameTx(ctx context.Context, tx bun.IDB, id int64, name string) (string, error) {
	var old string
	err := tx.NewSelect().
		Model((*domain.Tag)(nil)).
		Column("name").
		Where("id = ?", id).
		For("UPDATE").
		Scan(ctx, &old)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrTagNotFound
	}
	if err != nil {
		return "", fmt.Errorf("lock tag: %w", err)
	}

	if _, err := tx.NewUpdate().
		Model((*domain.Tag)(nil)).
		Set("name = ?", name).
		Where("id = ?", id).
		Exec(ctx); err != nil {
		return "", mapTagWriteError(err, "rename tag")
	}
	return old, nil
}

// SetHiddenTx скрывает тег или возвращает его в каталог. Идемпотентно:
// повторное скрытие скрытого тега — не ошибка.
func (r *TagRepo) SetHiddenTx(ctx context.Context, tx bun.IDB, id int64, hidden bool) error {
	res, err := tx.NewUpdate().
		Model((*domain.Tag)(nil)).
		Set("is_hidden = ?", hidden).
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("set tag hidden: %w", err)
	}
	return expectOneRow(res, ErrTagNotFound)
}

// mapTagWriteError распознаёт занятое имя. Оба индекса — и старый
// регистрозависимый tags_name_key, и uq_tags_name_lower — означают одно.
func mapTagWriteError(err error, op string) error {
	if name, ok := uniqueViolation(err); ok &&
		(strings.Contains(name, "uq_tags_name_lower") || strings.Contains(name, "tags_name_key")) {
		return ErrTagNameTaken
	}
	return fmt.Errorf("%s: %w", op, err)
}
