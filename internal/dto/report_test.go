package dto

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func validReport() CreateReportRequest {
	return CreateReportRequest{TargetType: "message", TargetID: 42, Reason: "spam"}
}

func TestCreateReportRequestValid(t *testing.T) {
	r := validReport()
	require.Empty(t, r.Validate())
}

func TestCreateReportRequestRequiresFields(t *testing.T) {
	r := CreateReportRequest{}
	errs := r.Validate()
	require.Contains(t, errs, "target_type")
	require.Contains(t, errs, "target_id")
	require.Contains(t, errs, "reason")
}

func TestCreateReportRequestRejectsNegativeID(t *testing.T) {
	r := validReport()
	r.TargetID = -1
	require.Contains(t, r.Validate(), "target_id")
}

// Лимит — в СИМВОЛАХ. Кириллица занимает 2 байта на символ, и проверка через
// len() отрезала бы русский комментарий вдвое раньше обещанного.
func TestCreateReportRequestCountsRunes(t *testing.T) {
	r := validReport()

	r.Comment = strings.Repeat("ж", MaxReportCommentRunes)
	require.Empty(t, r.Validate(), "ровно 1000 кириллических символов — допустимо")

	r.Comment = strings.Repeat("ж", MaxReportCommentRunes+1)
	require.Contains(t, r.Validate(), "comment")
}
