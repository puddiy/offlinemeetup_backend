package dto

import "time"

// AdminUserRow — строка админского списка пользователей.
//
// Отдельный тип, а не domain.User: список показывает поддержке ровно то,
// что нужно для принятия решения, и не тащит в шаблон ни хэш пароля
// (его тут и нет), ни связи, которые списку не нужны.
type AdminUserRow struct {
	ID          int64      `json:"id"`
	Email       string     `json:"email"`
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	DeletedAt   *time.Time `json:"deleted_at"`
}

// IsDeleted — удобство для шаблона: {{if .IsDeleted}} читается лучше,
// чем {{if .DeletedAt}}.
func (r AdminUserRow) IsDeleted() bool { return r.DeletedAt != nil }
