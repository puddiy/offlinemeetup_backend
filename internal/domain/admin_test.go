package domain

import "testing"

func TestAdminRoleValid(t *testing.T) {
	cases := []struct {
		name string
		role AdminRole
		want bool
	}{
		{"moderator", AdminRoleModerator, true},
		{"admin", AdminRoleAdmin, true},
		{"empty", AdminRole(""), false},
		{"unknown", AdminRole("superuser"), false},
		{"case sensitive", AdminRole("Admin"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.role.Valid(); got != tc.want {
				t.Fatalf("AdminRole(%q).Valid() = %v, want %v", tc.role, got, tc.want)
			}
		})
	}
}
