package domain

import (
	"time"

	"github.com/uptrace/bun"
)

// AdminRole — роль в админ-контуре. Специально НЕ переиспользует users.role:
// админы живут в отдельной таблице (см. миграцию 20260916120000), поэтому
// компрометация мобильного JWT не даёт доступа к панели.
type AdminRole string

const (
	// AdminRoleModerator — разбор жалоб, скрытие контента, бан пользователей.
	AdminRoleModerator AdminRole = "moderator"
	// AdminRoleAdmin — всё, что может модератор, плюс управление админами
	// и контент-менеджмент.
	AdminRoleAdmin AdminRole = "admin"
)

// Valid сообщает, что роль — одна из известных. Нужна на границах ввода
// (CLI создания админа, форма редактирования), чтобы в БД не приехала
// строка, под которую ни одна проверка прав не сработает.
func (r AdminRole) Valid() bool {
	return r == AdminRoleModerator || r == AdminRoleAdmin
}

// AdminUser — учётка администратора. Пароль хранится bcrypt-хэшем (cost 12,
// как у обычных пользователей — internal/service/auth_password.go).
type AdminUser struct {
	bun.BaseModel `bun:"table:admin_users" swaggerignore:"true"`

	ID           int64     `bun:",pk,autoincrement"`
	Email        string    `bun:",unique,notnull"`
	PasswordHash string    `bun:",notnull"`
	Role         AdminRole `bun:",type:varchar(20),notnull"`
	IsActive     bool      `bun:",notnull,default:true"`
	CreatedAt    time.Time `bun:",nullzero,notnull,default:current_timestamp"`
	UpdatedAt    time.Time `bun:",nullzero,notnull,default:current_timestamp"`
}

// AuditEntry — одна запись журнала действий администратора. TargetID — строка,
// а не int64, потому что цели разнородны: митап (bigint), файл (uuid),
// пользователь (bigint). Details — свободный jsonb со снимком «что именно
// поменяли», чтобы по журналу можно было восстановить картину без исходных
// строк, которые к тому моменту могут быть уже удалены.
type AuditEntry struct {
	bun.BaseModel `bun:"table:admin_audit_log" swaggerignore:"true"`

	ID         int64          `bun:",pk,autoincrement"`
	AdminID    int64          `bun:",notnull"`
	Action     string         `bun:",notnull"`
	TargetType string         `bun:",nullzero"`
	TargetID   string         `bun:",nullzero"`
	Details    map[string]any `bun:"type:jsonb,nullzero"`
	IP         string         `bun:",nullzero"`
	CreatedAt  time.Time      `bun:",nullzero,notnull,default:current_timestamp"`
}
