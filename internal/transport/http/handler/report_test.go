package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
	"github.com/stretchr/testify/require"
)

type stubReportSvc struct {
	gotReporter int64
	gotReq      dto.CreateReportRequest
	id          int64
	err         error
}

func (s *stubReportSvc) CreateReport(_ context.Context, reporterID int64, req dto.CreateReportRequest) (int64, error) {
	s.gotReporter, s.gotReq = reporterID, req
	return s.id, s.err
}

func (s *stubReportSvc) Reasons() []dto.ReportReasonResponse {
	return []dto.ReportReasonResponse{{Code: "spam", Title: "Спам или реклама"}}
}

func reportPost(userID int64, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/reports", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if userID != 0 {
		req = req.WithContext(context.WithValue(req.Context(), middleware.UserIDKey, userID))
	}
	return req
}

const validReportBody = `{"target_type":"message","target_id":42,"reason":"spam","comment":"реклама"}`

func TestReportCreateReturns201WithID(t *testing.T) {
	svc := &stubReportSvc{id: 17}
	h := NewReportHandler(svc, slog.New(slog.DiscardHandler))

	rec := httptest.NewRecorder()
	h.Create(rec, reportPost(5, validReportBody))

	require.Equal(t, http.StatusCreated, rec.Code)
	var resp dto.CreateReportResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, int64(17), resp.ID)
	require.Equal(t, int64(5), svc.gotReporter)
	require.Equal(t, "message", svc.gotReq.TargetType)
}

func TestReportCreateRequiresAuth(t *testing.T) {
	svc := &stubReportSvc{}
	h := NewReportHandler(svc, slog.New(slog.DiscardHandler))

	rec := httptest.NewRecorder()
	h.Create(rec, reportPost(0, validReportBody))

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Zero(t, svc.gotReporter)
}

func TestReportCreateBadJSONIs400(t *testing.T) {
	h := NewReportHandler(&stubReportSvc{}, slog.New(slog.DiscardHandler))

	rec := httptest.NewRecorder()
	h.Create(rec, reportPost(5, `{"target_id":`))

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestReportCreateValidationListsFields(t *testing.T) {
	h := NewReportHandler(&stubReportSvc{}, slog.New(slog.DiscardHandler))

	rec := httptest.NewRecorder()
	h.Create(rec, reportPost(5, `{}`))

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "target_type")
}

func TestReportCreateMapsServiceErrors(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{service.ErrNotFound, http.StatusNotFound},
		{service.ErrAlreadyExists, http.StatusConflict},
		{service.ErrInvalidInput, http.StatusBadRequest},
		{errors.New("db down"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		h := NewReportHandler(&stubReportSvc{err: tc.err}, slog.New(slog.DiscardHandler))
		rec := httptest.NewRecorder()
		h.Create(rec, reportPost(5, validReportBody))
		require.Equal(t, tc.want, rec.Code, "%v", tc.err)
	}
}

func TestReportReasonsReturnsList(t *testing.T) {
	h := NewReportHandler(&stubReportSvc{}, slog.New(slog.DiscardHandler))

	rec := httptest.NewRecorder()
	h.Reasons(rec, httptest.NewRequest(http.MethodGet, "/v1/reports/reasons", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var got []dto.ReportReasonResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "spam", got[0].Code)
}
