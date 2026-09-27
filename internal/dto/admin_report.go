package dto

import "time"

// AdminReportRow — строка очереди жалоб в админке.
type AdminReportRow struct {
	ID            int64     `json:"id"`
	TargetType    string    `json:"target_type"`
	TargetID      int64     `json:"target_id"`
	TargetOwnerID int64     `json:"target_owner_id"`
	ReporterID    int64     `json:"reporter_id"`
	Reason        string    `json:"reason"`
	ReasonTitle   string    `json:"reason_title"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
}

// IsOpen — удобство для шаблона: действия показываются только у открытых.
// Строка "open", а не domain.ReportStatusOpen: dto не зависит от domain.
func (r AdminReportRow) IsOpen() bool { return r.Status == "open" }

// AdminReportDetail — карточка жалобы. SnapshotText / SnapshotFileURL — то,
// что видел жалующийся в момент подачи, а не текущее состояние контента.
type AdminReportDetail struct {
	AdminReportRow
	Comment         string     `json:"comment"`
	SnapshotText    string     `json:"snapshot_text"`
	SnapshotFileURL string     `json:"snapshot_file_url"`
	Resolution      string     `json:"resolution"`
	ResolvedBy      *int64     `json:"resolved_by"`
	ResolvedAt      *time.Time `json:"resolved_at"`
}
