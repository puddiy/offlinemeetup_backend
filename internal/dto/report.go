package dto

import "unicode/utf8"

// MaxReportCommentRunes — предел комментария к жалобе, в символах.
const MaxReportCommentRunes = 1000

// CreateReportRequest — жалоба с мобильного клиента.
//
// Значения target_type и reason здесь проверяются только на непустоту: их
// допустимые наборы живут в domain, а dto от domain не зависит. Проверку
// набора делает сервис (ErrInvalidInput → 400).
type CreateReportRequest struct {
	TargetType string `json:"target_type" example:"message" enums:"meetup,message,user"`
	TargetID   int64  `json:"target_id" example:"42"`
	Reason     string `json:"reason" example:"spam" enums:"spam,harassment,hate,sexual,violence,scam,other"`
	Comment    string `json:"comment,omitempty" example:"Присылает рекламу казино"`
}

func (r *CreateReportRequest) Validate() map[string]string {
	errs := make(map[string]string)
	if r.TargetType == "" {
		errs["target_type"] = "required"
	}
	if r.TargetID <= 0 {
		errs["target_id"] = "must be positive"
	}
	if r.Reason == "" {
		errs["reason"] = "required"
	}
	if utf8.RuneCountInString(r.Comment) > MaxReportCommentRunes {
		errs["comment"] = "must be at most 1000 chars"
	}
	return errs
}

// CreateReportResponse — id созданной жалобы.
type CreateReportResponse struct {
	ID int64 `json:"id" example:"17"`
}

// ReportReasonResponse — пункт списка причин для экрана жалобы.
type ReportReasonResponse struct {
	Code  string `json:"code" example:"spam"`
	Title string `json:"title" example:"Спам или реклама"`
}
