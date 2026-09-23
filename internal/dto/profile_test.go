package dto

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidUsername(t *testing.T) {
	tests := []struct {
		name     string
		username string
		want     bool
	}{
		{name: "simple lowercase", username: "bob", want: true},
		{name: "with underscore and digit", username: "bob_2", want: true},
		{name: "with dot", username: "bob.smith", want: true},
		{name: "minimum length (2 chars)", username: "bo", want: true},
		{name: "maximum length (32 chars)", username: strings.Repeat("a", 32), want: true},
		{name: "too short (1 char)", username: "b", want: false},
		{name: "too long (33 chars)", username: strings.Repeat("a", 33), want: false},
		{name: "empty string", username: "", want: false},
		{
			// Load-bearing for isEmailLogin's email-vs-username
			// disambiguation (ADR-2): '@' must never be a valid username
			// character, or the login endpoint couldn't tell the two
			// apart by presence of '@'.
			name:     "contains @ must be rejected",
			username: "bob@example.com",
			want:     false,
		},
		{name: "contains space", username: "bob smith", want: false},
		{name: "contains disallowed symbol", username: "bob!smith", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ValidUsername(tt.username))
		})
	}
}

// TestValidUsernameRejectsAnonymizedForm закрепляет инвариант, на котором
// держится удаление аккаунта.
//
// repo.anonymizedUsername переименовывает профиль удалённого пользователя в
// «deleted-<id>». Безопасность этой схемы держится ровно на одном факте: дефис
// НЕ входит в набор символов, разрешённый для пользовательского username.
// Поэтому занять такое имя заранее нельзя, и UPDATE при удалении не может
// упереться в уникальный индекс uq_profile_username_lower.
//
// Если кто-то когда-нибудь добавит дефис в usernameRegexp — этот тест упадёт,
// и упадёт он здесь, а не в проде на пользователе, который не может удалить
// свой аккаунт. Раньше разделителем было подчёркивание, и «deleted_42»
// прекрасно регистрировался: удаление аккаунта ломалось навсегда.
func TestValidUsernameRejectsAnonymizedForm(t *testing.T) {
	anonymized := []string{"deleted-1", "deleted-42", "deleted-9223372036854775807"}
	for _, name := range anonymized {
		if ValidUsername(name) {
			t.Fatalf("ValidUsername(%q) = true; анонимизированное имя обязано быть незанимаемым", name)
		}
	}

	// Контроль: подчёркивание всё ещё валидно, то есть тест выше падает
	// именно из-за дефиса, а не потому что regexp сломан целиком.
	if !ValidUsername("deleted_42") {
		t.Fatal(`ValidUsername("deleted_42") = false; ожидался валидный username — иначе тест выше ничего не доказывает`)
	}
}
