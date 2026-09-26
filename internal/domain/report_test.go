package domain

import "testing"

// Список причин отдаётся клиенту как есть (GET /v1/reports/reasons), поэтому
// каждая причина обязана быть валидной и иметь человеческую подпись — иначе
// в приложении появится пункт с пустым текстом или с кодом вместо текста.
func TestReportReasonsAreValidAndTitled(t *testing.T) {
	if len(ReportReasons) == 0 {
		t.Fatal("ReportReasons пуст")
	}
	seen := make(map[ReportReason]bool, len(ReportReasons))
	for _, r := range ReportReasons {
		if !r.Valid() {
			t.Errorf("причина %q из списка не проходит Valid()", r)
		}
		if r.Title() == "" || r.Title() == string(r) {
			t.Errorf("причина %q без человеческой подписи: %q", r, r.Title())
		}
		if seen[r] {
			t.Errorf("причина %q повторяется", r)
		}
		seen[r] = true
	}
	if last := ReportReasons[len(ReportReasons)-1]; last != ReportReasonOther {
		t.Errorf("«Другое» обязано быть последним пунктом, а последний — %q", last)
	}
}

func TestReportReasonValidRejectsUnknown(t *testing.T) {
	for _, r := range []ReportReason{"", "SPAM", "nudity", "other "} {
		if r.Valid() {
			t.Errorf("ReportReason(%q).Valid() = true, want false", r)
		}
		if r.Title() != "" {
			t.Errorf("у неизвестной причины %q не должно быть подписи", r)
		}
	}
}

func TestReportTargetTypeValid(t *testing.T) {
	cases := []struct {
		typ  ReportTargetType
		want bool
	}{
		{ReportTargetMeetup, true},
		{ReportTargetMessage, true},
		{ReportTargetUser, true},
		{"", false},
		{"chat", false},
		{"Meetup", false},
	}
	for _, tc := range cases {
		if got := tc.typ.Valid(); got != tc.want {
			t.Errorf("ReportTargetType(%q).Valid() = %v, want %v", tc.typ, got, tc.want)
		}
	}
}
