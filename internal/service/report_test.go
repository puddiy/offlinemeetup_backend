package service

import (
	"context"
	"errors"
	"testing"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo/mocks"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func setupReportTest(t *testing.T) (*mocks.MockReportCreator, *ReportService) {
	t.Helper()
	m := mocks.NewMockReportCreator(gomock.NewController(t))
	return m, NewReportService(m)
}

func TestCreateReportPassesFieldsAndReturnsID(t *testing.T) {
	m, svc := setupReportTest(t)

	m.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, rep *domain.Report) error {
			require.Equal(t, int64(5), rep.ReporterID)
			require.Equal(t, domain.ReportTargetMessage, rep.TargetType)
			require.Equal(t, int64(42), rep.TargetID)
			require.Equal(t, domain.ReportReasonSpam, rep.Reason)
			require.Equal(t, "реклама", rep.Comment, "пробелы по краям обрезаются")
			rep.ID = 77
			return nil
		})

	id, err := svc.CreateReport(context.Background(), 5, dto.CreateReportRequest{
		TargetType: "message", TargetID: 42, Reason: "spam", Comment: "  реклама \n",
	})

	require.NoError(t, err)
	require.Equal(t, int64(77), id)
}

// Неизвестные значения не доезжают до БД: мок без EXPECT провалит тест,
// если Create всё-таки вызовут.
func TestCreateReportRejectsUnknownEnums(t *testing.T) {
	cases := []dto.CreateReportRequest{
		{TargetType: "chat", TargetID: 1, Reason: "spam"},
		{TargetType: "message", TargetID: 1, Reason: "nudity"},
		{TargetType: "message", TargetID: 0, Reason: "spam"},
	}
	for _, req := range cases {
		_, svc := setupReportTest(t)
		_, err := svc.CreateReport(context.Background(), 5, req)
		require.ErrorIs(t, err, ErrInvalidInput, "%+v", req)
	}
}

func TestCreateReportMapsRepoErrors(t *testing.T) {
	boom := errors.New("db down")
	cases := []struct {
		name    string
		repoErr error
		want    error
	}{
		// Невидимая цель и несуществующая — один ответ 404 (см. ErrReportTargetNotFound).
		{"not visible", repo.ErrReportTargetNotFound, ErrNotFound},
		{"own content", repo.ErrReportOwnTarget, ErrInvalidInput},
		{"duplicate", repo.ErrReportDuplicate, ErrAlreadyExists},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, svc := setupReportTest(t)
			m.EXPECT().Create(gomock.Any(), gomock.Any()).Return(tc.repoErr)

			_, err := svc.CreateReport(context.Background(), 5, dto.CreateReportRequest{
				TargetType: "user", TargetID: 9, Reason: "scam",
			})
			require.ErrorIs(t, err, tc.want)
		})
	}

	t.Run("unknown error is not a sentinel", func(t *testing.T) {
		m, svc := setupReportTest(t)
		m.EXPECT().Create(gomock.Any(), gomock.Any()).Return(boom)

		_, err := svc.CreateReport(context.Background(), 5, dto.CreateReportRequest{
			TargetType: "user", TargetID: 9, Reason: "scam",
		})
		require.ErrorIs(t, err, boom)
		require.NotErrorIs(t, err, ErrNotFound)
		require.NotErrorIs(t, err, ErrInvalidInput)
	})
}

func TestReportReasonsListsAllInOrder(t *testing.T) {
	_, svc := setupReportTest(t)

	got := svc.Reasons()

	require.Len(t, got, len(domain.ReportReasons))
	for i, r := range domain.ReportReasons {
		require.Equal(t, string(r), got[i].Code)
		require.Equal(t, r.Title(), got[i].Title)
	}
}
