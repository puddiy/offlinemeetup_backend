package admin

import (
	"testing"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestBadgeKnownValues(t *testing.T) {
	cases := []struct {
		kind  string
		value any
		text  string
		tone  string
	}{
		{"user_status", domain.UserStatusBanned, "заблокирован", "danger"},
		{"user_status", "active", "активен", "success"},
		{"meetup_status", "past", "прошёл", "neutral"},
		{"meetup_status", "cancelled", "отменён", "danger"},
		{"report_status", "open", "открыта", "warning"},
		{"report_target", "message", "сообщение", "neutral"},
		{"admin_role", domain.AdminRoleModerator, "модератор", "neutral"},
	}
	for _, tc := range cases {
		t.Run(tc.kind+"/"+tc.text, func(t *testing.T) {
			got := string(badge(tc.kind, tc.value))
			require.Contains(t, got, `class="badge badge-`+tc.tone+`"`)
			require.Contains(t, got, ">"+tc.text+"<")
		})
	}
}

// Review Focus #2: значение, которого нет в каталоге, не роняет страницу и
// не превращается в разметку.
func TestBadgeUnknownValueIsEscaped(t *testing.T) {
	got := string(badge("user_status", `<img src=x onerror=alert(1)>`))
	require.Contains(t, got, "badge-neutral")
	require.Contains(t, got, "&lt;img")
	require.NotContains(t, got, "<img")
}

func TestMarkUnknownToneFallsBackToNeutral(t *testing.T) {
	got := string(mark(`danger" onclick="x`, "<b>текст</b>"))
	require.Contains(t, got, `class="badge badge-neutral"`)
	require.Contains(t, got, "&lt;b&gt;текст&lt;/b&gt;")
	require.NotContains(t, got, "onclick")
}
