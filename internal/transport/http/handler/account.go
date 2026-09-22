package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/response"
)

// AccountDeleter — то, что хендлер требует от сервиса.
type AccountDeleter interface {
	DeleteOwnAccount(ctx context.Context, userID int64) error
}

type AccountHandler struct {
	svc AccountDeleter
	log *slog.Logger
}

func NewAccountHandler(svc AccountDeleter, log *slog.Logger) *AccountHandler {
	return &AccountHandler{svc: svc, log: log}
}

// DeleteMyAccount godoc
// @Summary      Удалить свой аккаунт
// @Description  Мягкое удаление с анонимизацией: email обнуляется, привязки соцсетей и пароль удаляются, все refresh-токены отзываются. Сообщения и прошедшие митапы остаются, автор отображается как «Удалённый пользователь». Восстановление невозможно.
// @Tags         account
// @Produce      json
// @Success      204  "Аккаунт удалён"
// @Failure      401  {object}  response.ErrorResponse
// @Failure      409  {object}  response.ErrorResponse  "Аккаунт уже удалён"
// @Failure      500  {object}  response.ErrorResponse
// @Security     BearerAuth
// @Router       /v1/account [delete]
func (h *AccountHandler) DeleteMyAccount(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r, h.log)
	if !ok {
		return
	}

	if err := h.svc.DeleteOwnAccount(r.Context(), userID); err != nil {
		// RespondError сам разложит сентинелы по кодам и выберет уровень
		// лога по классу статуса — голый fmt.Errorf сюда передавать нельзя,
		// он не сентинел и уедет в 500.
		response.RespondError(w, err, h.log)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
