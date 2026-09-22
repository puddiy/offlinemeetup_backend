package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
	"github.com/stretchr/testify/require"
)

type stubAccountSvc struct {
	gotUserID int64
	err       error
}

func (s *stubAccountSvc) DeleteOwnAccount(_ context.Context, userID int64) error {
	s.gotUserID = userID
	return s.err
}

func authedDelete(userID int64) *http.Request {
	req := httptest.NewRequest(http.MethodDelete, "/v1/account", nil)
	if userID != 0 {
		req = req.WithContext(context.WithValue(req.Context(), middleware.UserIDKey, userID))
	}
	return req
}

func TestDeleteMyAccountSuccess(t *testing.T) {
	svc := &stubAccountSvc{}
	h := NewAccountHandler(svc, slog.New(slog.DiscardHandler))

	rec := httptest.NewRecorder()
	h.DeleteMyAccount(rec, authedDelete(42))

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, int64(42), svc.gotUserID)
}

func TestDeleteMyAccountUnauthenticated(t *testing.T) {
	svc := &stubAccountSvc{}
	h := NewAccountHandler(svc, slog.New(slog.DiscardHandler))

	rec := httptest.NewRecorder()
	h.DeleteMyAccount(rec, authedDelete(0))

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Zero(t, svc.gotUserID)
}

func TestDeleteMyAccountAlreadyDeletedIsConflict(t *testing.T) {
	svc := &stubAccountSvc{err: service.ErrUserAlreadyDeleted}
	h := NewAccountHandler(svc, slog.New(slog.DiscardHandler))

	rec := httptest.NewRecorder()
	h.DeleteMyAccount(rec, authedDelete(42))

	require.Equal(t, http.StatusConflict, rec.Code)
}

// Голая ошибка обязана стать 500, а не утечь текстом наружу.
func TestDeleteMyAccountUnknownErrorIs500(t *testing.T) {
	svc := &stubAccountSvc{err: errors.New("db down")}
	h := NewAccountHandler(svc, slog.New(slog.DiscardHandler))

	rec := httptest.NewRecorder()
	h.DeleteMyAccount(rec, authedDelete(42))

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.NotContains(t, rec.Body.String(), "db down")
}
