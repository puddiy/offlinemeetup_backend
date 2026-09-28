package service

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/cache"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo/mocks"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"go.uber.org/mock/gomock"
)

type adminTagFixture struct {
	repo  *mocks.MockAdminTagRepository
	audit *recordingAuditSvc
	mr    *miniredis.Miniredis
	svc   *AdminTagService
}

func setupAdminTagTest(t *testing.T) *adminTagFixture {
	t.Helper()
	ctrl := gomock.NewController(t)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	log := slog.New(slog.DiscardHandler)
	tc := cache.NewTagCache(cache.NewRedisCache(rdb, log), cache.NopMetrics, time.Minute)

	f := &adminTagFixture{repo: mocks.NewMockAdminTagRepository(ctrl), audit: &recordingAuditSvc{}, mr: mr}
	f.svc = NewAdminTagService(f.repo, f.audit, tc, log)
	return f
}

// expectTagTx — RunInTx просто исполняет замыкание.
func expectTagTx(f *adminTagFixture) {
	f.repo.EXPECT().RunInTx(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, fn func(tx bun.Tx) error) error {
			return fn(bun.Tx{})
		})
}

// warmCatalog кладёт каталог в кэш, чтобы проверить, что мутация его сбросила.
func warmCatalog(t *testing.T, f *adminTagFixture) {
	t.Helper()
	require.NoError(t, f.mr.Set(cache.TagsKey(), "[]"))
}

func TestAdminTagCreateNormalizesAuditsAndDropsCatalog(t *testing.T) {
	f := setupAdminTagTest(t)
	warmCatalog(t, f)
	expectTagTx(f)
	f.repo.EXPECT().CreateTx(gomock.Any(), gomock.Any(), "Настольные игры").
		Return(&domain.Tag{ID: 11, Name: "Настольные игры"}, nil)

	require.NoError(t, f.svc.Create(context.Background(), 7, "  Настольные \t  игры ", "1.2.3.4"))

	require.Len(t, f.audit.events, 1)
	ev := f.audit.events[0]
	require.Equal(t, AuditActionTagCreate, ev.Action)
	require.Equal(t, "tag", ev.TargetType)
	require.Equal(t, "11", ev.TargetID)
	require.Equal(t, "Настольные игры", ev.Details["name"])
	require.False(t, f.mr.Exists(cache.TagsKey()), "каталог обязан сброситься")
}

func TestAdminTagCreateRejectsBadNames(t *testing.T) {
	for _, name := range []string{"", "  ", "a", strings.Repeat("я", 41)} {
		f := setupAdminTagTest(t)
		require.ErrorIs(t, f.svc.Create(context.Background(), 7, name, ""), ErrInvalidInput, "name=%q", name)
		require.Empty(t, f.audit.events)
	}
}

// Review Focus #5: регистровый дубль ловит индекс lower(name).
func TestAdminTagCreateTakenNameIsAlreadyExists(t *testing.T) {
	f := setupAdminTagTest(t)
	warmCatalog(t, f)
	expectTagTx(f)
	f.repo.EXPECT().CreateTx(gomock.Any(), gomock.Any(), "спорт").Return(nil, repo.ErrTagNameTaken)

	require.ErrorIs(t, f.svc.Create(context.Background(), 7, "спорт", ""), ErrAlreadyExists)
	require.Empty(t, f.audit.events)
	require.True(t, f.mr.Exists(cache.TagsKey()), "неудачная мутация кэш не трогает")
}

func TestAdminTagRenameRecordsFromTo(t *testing.T) {
	f := setupAdminTagTest(t)
	expectTagTx(f)
	f.repo.EXPECT().RenameTx(gomock.Any(), gomock.Any(), int64(4), "Кино").Return("Фильмы", nil)

	require.NoError(t, f.svc.Rename(context.Background(), 7, 4, "Кино", ""))

	ev := f.audit.events[0]
	require.Equal(t, AuditActionTagRename, ev.Action)
	require.Equal(t, "Фильмы", ev.Details["from"])
	require.Equal(t, "Кино", ev.Details["to"])
}

func TestAdminTagRenameErrors(t *testing.T) {
	t.Run("rename to taken name", func(t *testing.T) {
		f := setupAdminTagTest(t)
		expectTagTx(f)
		f.repo.EXPECT().RenameTx(gomock.Any(), gomock.Any(), int64(4), "Спорт").Return("", repo.ErrTagNameTaken)
		require.ErrorIs(t, f.svc.Rename(context.Background(), 7, 4, "Спорт", ""), ErrAlreadyExists)
	})
	t.Run("missing tag", func(t *testing.T) {
		f := setupAdminTagTest(t)
		expectTagTx(f)
		f.repo.EXPECT().RenameTx(gomock.Any(), gomock.Any(), int64(404), "Кино").Return("", repo.ErrTagNotFound)
		require.ErrorIs(t, f.svc.Rename(context.Background(), 7, 404, "Кино", ""), ErrNotFound)
	})
}

func TestAdminTagSetHidden(t *testing.T) {
	cases := []struct {
		hidden bool
		action string
	}{
		{true, AuditActionTagHide},
		{false, AuditActionTagUnhide},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			f := setupAdminTagTest(t)
			warmCatalog(t, f)
			expectTagTx(f)
			f.repo.EXPECT().SetHiddenTx(gomock.Any(), gomock.Any(), int64(4), tc.hidden).Return(nil)

			require.NoError(t, f.svc.SetHidden(context.Background(), 7, 4, tc.hidden, ""))
			require.Equal(t, tc.action, f.audit.events[0].Action)
			require.False(t, f.mr.Exists(cache.TagsKey()))
		})
	}
}

func TestAdminTagListMapsUsage(t *testing.T) {
	f := setupAdminTagTest(t)
	f.repo.EXPECT().ListWithUsage(gomock.Any()).Return([]repo.TagUsage{
		{ID: 1, Name: "Спорт", Meetups: 3, Users: 10},
		{ID: 2, Name: "Старое", IsHidden: true, Meetups: 1},
	}, nil)

	rows, err := f.svc.List(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "Спорт", rows[0].Name)
	require.Equal(t, 10, rows[0].Users)
	require.True(t, rows[1].IsHidden)
}
