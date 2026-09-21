package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/uptrace/bun"
)

var (
	ErrUserNotFound       = errors.New("user not found")
	ErrUserAlreadyDeleted = errors.New("user already deleted")
)

// UserQuery — критерии админского поиска. Как и MeetupQuery, это
// плоская структура без json-тегов: форма запроса по проводу — забота
// транспорта, слой SQL от неё зависеть не должен.
type UserQuery struct {
	// Search совпадает с email, profile.username или числовым id.
	Search string
	// Status — точное значение users.status ("" = любой).
	Status string
	// IncludeDeleted подмешивает удалённых к остальным.
	// OnlyDeleted показывает ТОЛЬКО удалённых (имеет приоритет).
	IncludeDeleted bool
	OnlyDeleted    bool

	Limit  int
	Offset int
}

type UserAdminRepo struct {
	db *bun.DB
}

func NewUserAdminRepo(db *bun.DB) *UserAdminRepo {
	return &UserAdminRepo{db: db}
}

// RunInTx выполняет fn в транзакции, откатывая её при любой ошибке.
//
// Нужен потому, что необратимое действие администратора и запись о нём в
// admin_audit_log обязаны быть атомарны: откат мутации без отката журнала
// оставил бы в нём след действия, которого не было.
func (r *UserAdminRepo) RunInTx(ctx context.Context, fn func(tx bun.Tx) error) error {
	return r.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return fn(tx)
	})
}

// List возвращает страницу пользователей и ОБЩЕЕ число подходящих строк.
//
// Total считается тем же запросом через ScanAndCount (Bun снимает LIMIT/OFFSET
// для счётной ветки), а не отдельным SELECT count(*): два запроса разъехались
// бы между собой при параллельной записи, и «страница 3 из 47» иногда
// оказывалась бы пустой.
func (r *UserAdminRepo) List(ctx context.Context, q UserQuery) ([]domain.User, int, error) {
	var users []domain.User

	query := r.db.NewSelect().
		Model(&users).
		Relation("Profile").
		Relation("Profile.AvatarFile")

	switch {
	case q.OnlyDeleted:
		query = query.Where("?TableAlias.deleted_at IS NOT NULL")
	case !q.IncludeDeleted:
		query = query.Where("?TableAlias.deleted_at IS NULL")
	}

	if q.Status != "" {
		query = query.Where("?TableAlias.status = ?", q.Status)
	}

	if search := strings.TrimSpace(q.Search); search != "" {
		// Точное совпадение по id, когда ввели число, плюс подстрока по
		// email и username. ILIKE, а не LIKE: поддержка ищет «Denis», а в
		// базе лежит «denis».
		pattern := "%" + search + "%"
		query = query.WhereGroup(" AND ", func(sq *bun.SelectQuery) *bun.SelectQuery {
			sq = sq.Where("?TableAlias.email ILIKE ?", pattern).
				WhereOr("profile.username ILIKE ?", pattern).
				WhereOr("profile.display_name ILIKE ?", pattern)
			if id, err := strconv.ParseInt(search, 10, 64); err == nil {
				sq = sq.WhereOr("?TableAlias.id = ?", id)
			}
			return sq
		})
	}

	query = query.OrderExpr("?TableAlias.id DESC")

	if q.Limit > 0 {
		query = query.Limit(q.Limit)
	}
	if q.Offset > 0 {
		query = query.Offset(q.Offset)
	}

	total, err := query.ScanAndCount(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}
	return users, total, nil
}

// GetDetail отдаёт одного пользователя со связями для карточки.
// Удалённые ТОЖЕ возвращаются: карточка удалённого аккаунта — это
// рабочий сценарий поддержки («что стало с этим пользователем»).
func (r *UserAdminRepo) GetDetail(ctx context.Context, id int64) (*domain.User, error) {
	user := new(domain.User)
	err := r.db.NewSelect().
		Model(user).
		Relation("Profile").
		Relation("Profile.AvatarFile").
		Relation("Socials").
		Relation("Tags").
		Where("?TableAlias.id = ?", id).
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user detail: %w", err)
	}
	return user, nil
}

// SetStatusTx меняет статус пользователя внутри переданной транзакции.
//
// Удалённый аккаунт статус не меняет: «разбанить удалённого» — бессмысленная
// операция, а возможность её выполнить создаёт впечатление, что аккаунт
// воскрешён, хотя входить в него по-прежнему нечем.
func (r *UserAdminRepo) SetStatusTx(ctx context.Context, tx bun.IDB, userID int64, status domain.UserStatus) error {
	res, err := tx.NewUpdate().
		Model((*domain.User)(nil)).
		Set("status = ?", status).
		Set("updated_at = ?", time.Now().UTC()).
		Where("id = ?", userID).
		Where("deleted_at IS NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("set user status: %w", err)
	}
	return expectOneRow(res, ErrUserNotFound)
}

// SoftDeleteTx анонимизирует аккаунт внутри переданной транзакции.
//
// Что происходит и почему именно так:
//   - users.deleted_at    — метка для админки;
//   - users.email = NULL  — снимает и идентификацию, и возможность найти
//     аккаунт по email при повторной регистрации. Колонка nullable, а оба
//     уникальных индекса допускают произвольное число NULL;
//   - users.status        — inactive, чтобы AuthMiddleware закрыл доступ
//     на следующем же запросе;
//   - profile.username    — deleted_<id> (формат уникален по построению),
//     display_name — «Удалённый пользователь»: DisplayNameOf покажет именно
//     это в истории чатов, где реплики остаются;
//   - profile.bio / avatar_file_id — вычищаются, это пользовательский контент;
//   - user_credentials    — удаляются: пароля больше нет;
//   - social_accounts     — удаляются, ИНАЧЕ повторный вход через Google
//     нашёл бы пару (provider, social_id) и воскресил бы удалённый аккаунт;
//   - refresh_tokens      — все отзываются, чтобы живые сессии умерли сразу.
//
// Сообщения и прошедшие митапы НЕ трогаем: это чужая история переписки,
// и вырезание реплик ломает контекст у собеседников.
func (r *UserAdminRepo) SoftDeleteTx(ctx context.Context, tx bun.IDB, userID int64) error {
	now := time.Now().UTC()

	res, err := tx.NewUpdate().
		Model((*domain.User)(nil)).
		Set("deleted_at = ?", now).
		Set("updated_at = ?", now).
		Set("email = NULL").
		Set("status = ?", domain.UserStatusInactive).
		Where("id = ?", userID).
		Where("deleted_at IS NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("soft delete user: %w", err)
	}
	if rowsErr := expectOneRow(res, ErrUserNotFound); rowsErr != nil {
		// Ноль строк здесь значит либо «нет такого», либо «уже удалён».
		// Различаем повторным чтением, чтобы поддержка видела осмысленное
		// сообщение вместо «пользователь не найден» на явно существующем id.
		exists, existsErr := tx.NewSelect().
			Model((*domain.User)(nil)).
			Where("id = ?", userID).
			Exists(ctx)
		if existsErr != nil {
			return fmt.Errorf("soft delete user: %w", existsErr)
		}
		if exists {
			return ErrUserAlreadyDeleted
		}
		return rowsErr
	}

	if _, err := tx.NewUpdate().
		Model((*domain.Profile)(nil)).
		Set("username = ?", "deleted_"+strconv.FormatInt(userID, 10)).
		Set("display_name = ?", "Удалённый пользователь").
		Set("bio = ?", "").
		Set("avatar_file_id = NULL").
		Set("updated_at = ?", now).
		Where("user_id = ?", userID).
		Exec(ctx); err != nil {
		return fmt.Errorf("anonymize profile: %w", err)
	}

	if _, err := tx.NewDelete().
		Model((*domain.UserCredentials)(nil)).
		Where("user_id = ?", userID).
		Exec(ctx); err != nil {
		return fmt.Errorf("drop credentials: %w", err)
	}

	if _, err := tx.NewDelete().
		Model((*domain.SocialAccount)(nil)).
		Where("user_id = ?", userID).
		Exec(ctx); err != nil {
		return fmt.Errorf("drop social accounts: %w", err)
	}

	if _, err := tx.NewUpdate().
		Model((*domain.RefreshToken)(nil)).
		Set("revoked_at = ?", now).
		Where("user_id = ?", userID).
		Where("revoked_at IS NULL").
		Exec(ctx); err != nil {
		return fmt.Errorf("revoke refresh tokens: %w", err)
	}

	return nil
}

// expectOneRow превращает «обновлено ноль строк» в сентинел. Bun не считает
// это ошибкой, а для нас «обновили несуществующего» — именно not found.
func expectOneRow(res sql.Result, notFound error) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if affected == 0 {
		return notFound
	}
	return nil
}
