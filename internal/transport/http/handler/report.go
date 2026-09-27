package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/response"
)

// ReportSvc — то, что хендлер требует от сервиса жалоб.
type ReportSvc interface {
	CreateReport(ctx context.Context, reporterID int64, req dto.CreateReportRequest) (int64, error)
	Reasons() []dto.ReportReasonResponse
}

type ReportHandler struct {
	svc ReportSvc
	log *slog.Logger
}

func NewReportHandler(svc ReportSvc, log *slog.Logger) *ReportHandler {
	return &ReportHandler{svc: svc, log: log}
}

// Create godoc
// @Summary      Пожаловаться на контент
// @Description  Жалоба на митап, сообщение или пользователя. Жаловаться можно только на то, что вам видно: сообщение — из чата, где вы участник; приватный митап — если вы его участник. Иначе 404, неотличимый от несуществующего id. На свой контент жаловаться нельзя (400). Одна открытая жалоба на одну цель (повтор — 409). Лимит — 10 жалоб в час на пользователя (429).
// @Tags         reports
// @Accept       json
// @Produce      json
// @Param        request  body      dto.CreateReportRequest  true  "Жалоба"
// @Success      201      {object}  dto.CreateReportResponse
// @Failure      400      {object}  response.ValidationErrorResponse
// @Failure      401      {object}  response.ErrorResponse
// @Failure      404      {object}  response.ErrorResponse  "Цель не найдена или не видна"
// @Failure      409      {object}  response.ErrorResponse  "Открытая жалоба уже есть"
// @Failure      429      {string}  string                  "Слишком много жалоб"
// @Security     BearerAuth
// @Router       /v1/reports [post]
func (h *ReportHandler) Create(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r, h.log)
	if !ok {
		return
	}

	var req dto.CreateReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.RespondError(w, service.ErrInvalidInput, h.log)
		return
	}
	if errs := req.Validate(); len(errs) > 0 {
		response.RespondValidation(w, errs)
		return
	}

	id, err := h.svc.CreateReport(r.Context(), userID, req)
	if err != nil {
		response.RespondError(w, err, h.log)
		return
	}

	response.JSON(w, http.StatusCreated, dto.CreateReportResponse{ID: id})
}

// Reasons godoc
// @Summary      Причины жалоб
// @Description  Список причин для экрана жалобы, в порядке показа. Клиент не хардкодит его: коды и подписи приходят с сервера.
// @Tags         reports
// @Produce      json
// @Success      200  {array}   dto.ReportReasonResponse
// @Failure      401  {object}  response.ErrorResponse
// @Security     BearerAuth
// @Router       /v1/reports/reasons [get]
func (h *ReportHandler) Reasons(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, h.svc.Reasons())
}
