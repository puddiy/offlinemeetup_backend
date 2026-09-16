package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

// fakeAuditWriter ловит то, что сервис отдал репозиторию.
type fakeAuditWriter struct {
	got *domain.AuditEntry
	err error
}

func (f *fakeAuditWriter) Record(_ context.Context, _ bun.IDB, e *domain.AuditEntry) error {
	f.got = e
	return f.err
}

func TestAuditRecordMapsEvent(t *testing.T) {
	w := &fakeAuditWriter{}
	svc := NewAuditService(w, slog.New(slog.DiscardHandler))

	err := svc.Record(context.Background(), nil, AuditEvent{
		AdminID:    42,
		Action:     AuditActionLogin,
		TargetType: "admin_user",
		TargetID:   "42",
		IP:         "10.0.0.1",
		Details:    map[string]any{"email": "a@x.io"},
	})

	require.NoError(t, err)
	require.NotNil(t, w.got)
	require.Equal(t, int64(42), w.got.AdminID)
	require.Equal(t, AuditActionLogin, w.got.Action)
	require.Equal(t, "admin_user", w.got.TargetType)
	require.Equal(t, "42", w.got.TargetID)
	require.Equal(t, "10.0.0.1", w.got.IP)
	require.Equal(t, "a@x.io", w.got.Details["email"])
}

func TestAuditRecordRejectsEmptyAction(t *testing.T) {
	w := &fakeAuditWriter{}
	svc := NewAuditService(w, slog.New(slog.DiscardHandler))

	err := svc.Record(context.Background(), nil, AuditEvent{AdminID: 1})

	require.ErrorIs(t, err, ErrInvalidInput)
	require.Nil(t, w.got, "пустое действие не должно доезжать до репозитория")
}

func TestAuditRecordRejectsZeroAdmin(t *testing.T) {
	w := &fakeAuditWriter{}
	svc := NewAuditService(w, slog.New(slog.DiscardHandler))

	err := svc.Record(context.Background(), nil, AuditEvent{Action: AuditActionLogin})

	require.ErrorIs(t, err, ErrInvalidInput)
	require.Nil(t, w.got)
}

func TestAuditRecordPropagatesRepoError(t *testing.T) {
	boom := errors.New("db down")
	w := &fakeAuditWriter{err: boom}
	svc := NewAuditService(w, slog.New(slog.DiscardHandler))

	err := svc.Record(context.Background(), nil, AuditEvent{AdminID: 1, Action: AuditActionLogin})

	require.ErrorIs(t, err, boom)
}
