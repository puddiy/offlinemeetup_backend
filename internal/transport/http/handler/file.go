package handler

import (
	"log/slog"
	"net/http"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
	response "github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/response"
)

// uploadMemoryBuffer — сколько байт multipart-парсер держит в памяти; всё сверх
// спиллится во временный файл (он seekable, что и нужно FileService.Upload).
const uploadMemoryBuffer = 16 << 20

type FileHandler struct {
	service       *service.FileService
	maxUploadSize int64
	log           *slog.Logger
}

func NewFileHandler(service *service.FileService, maxUploadSize int64, log *slog.Logger) *FileHandler {
	return &FileHandler{service: service, maxUploadSize: maxUploadSize, log: log}
}

// Upload
// @Summary Загрузить медиафайл (фото/видео/аудио)
// @Security BearerAuth
// @Tags 	Files
// @Accept	multipart/form-data
// @Produce	json
// @Param   file formData file true "Медиафайл"
// @Success 201 {object} map[string]interface{}
// @Failure 400 {object} response.ErrorResponse
// @Router /v1/files/upload [post]
func (h *FileHandler) Upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, h.maxUploadSize)

	if err := r.ParseMultipartForm(uploadMemoryBuffer); err != nil {
		response.RespondError(w, service.ErrInvalidInput, h.log)
		return
	}

	userID, ok := middleware.GetUserIDFromContext(r.Context())
	if !ok {
		response.RespondError(w, service.ErrUnauthorized, h.log)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		response.RespondError(w, service.ErrInvalidInput, h.log)
		return
	}
	defer func() { _ = file.Close() }()

	res, err := h.service.Upload(r.Context(), userID, header.Filename, header.Size, file)
	if err != nil {
		response.RespondError(w, err, h.log)
		return
	}

	response.JSON(w, http.StatusCreated, map[string]interface{}{
		"id": res.ID,
	})
}
