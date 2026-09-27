package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo"
)

// ReportCreator — то, что ReportService требует от хранилища жалоб.
// Удовлетворяется *repo.ReportRepo.
type ReportCreator interface {
	Create(ctx context.Context, rep *domain.Report) error
}

type ReportService struct {
	repo ReportCreator
}

func NewReportService(r ReportCreator) *ReportService {
	return &ReportService{repo: r}
}

// CreateReport подаёт жалобу. Возвращает её id.
//
// Проверку видимости цели делает репозиторий, в той же транзакции, что и
// вставку. Сервис лишь валидирует наборы значений и переводит сентинелы:
// невидимая и несуществующая цель одинаково дают ErrNotFound (404).
func (s *ReportService) CreateReport(ctx context.Context, reporterID int64, req dto.CreateReportRequest) (int64, error) {
	if reporterID == 0 {
		return 0, ErrUnauthorized
	}

	targetType := domain.ReportTargetType(req.TargetType)
	reason := domain.ReportReason(req.Reason)
	if !targetType.Valid() || !reason.Valid() || req.TargetID <= 0 {
		return 0, fmt.Errorf("report: %w", ErrInvalidInput)
	}

	rep := &domain.Report{
		ReporterID: reporterID,
		TargetType: targetType,
		TargetID:   req.TargetID,
		Reason:     reason,
		Comment:    strings.TrimSpace(req.Comment),
	}

	switch err := s.repo.Create(ctx, rep); {
	case err == nil:
		return rep.ID, nil
	case errors.Is(err, repo.ErrReportTargetNotFound):
		return 0, fmt.Errorf("report target: %w", ErrNotFound)
	case errors.Is(err, repo.ErrReportOwnTarget):
		return 0, fmt.Errorf("report own content: %w", ErrInvalidInput)
	case errors.Is(err, repo.ErrReportDuplicate):
		return 0, fmt.Errorf("report: %w", ErrAlreadyExists)
	default:
		return 0, fmt.Errorf("create report: %w", err)
	}
}

// Reasons — список причин для экрана жалобы, в порядке показа.
func (s *ReportService) Reasons() []dto.ReportReasonResponse {
	out := make([]dto.ReportReasonResponse, 0, len(domain.ReportReasons))
	for _, r := range domain.ReportReasons {
		out = append(out, dto.ReportReasonResponse{Code: string(r), Title: r.Title()})
	}
	return out
}
