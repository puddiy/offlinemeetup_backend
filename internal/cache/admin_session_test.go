package cache

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newTestAdminSessionStore(t *testing.T) (*miniredis.Miniredis, *RedisAdminSessionStore) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return mr, NewRedisAdminSessionStore(rdb, slog.New(slog.DiscardHandler))
}

func TestAdminSessionSaveGet(t *testing.T) {
	mr, store := newTestAdminSessionStore(t)
	defer mr.Close()

	ctx := context.Background()
	issued := time.Now().UTC().Truncate(time.Second)
	want := AdminSession{AdminID: 7, Role: "admin", IssuedAt: issued}

	require.NoError(t, store.Save(ctx, "hash-1", want, time.Hour))

	got, found, err := store.Get(ctx, "hash-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, want.AdminID, got.AdminID)
	require.Equal(t, want.Role, got.Role)
	require.True(t, want.IssuedAt.Equal(got.IssuedAt))
}

func TestAdminSessionGetMissing(t *testing.T) {
	mr, store := newTestAdminSessionStore(t)
	defer mr.Close()

	_, found, err := store.Get(context.Background(), "nope")
	require.NoError(t, err)
	require.False(t, found)
}

func TestAdminSessionExpires(t *testing.T) {
	mr, store := newTestAdminSessionStore(t)
	defer mr.Close()

	ctx := context.Background()
	require.NoError(t, store.Save(ctx, "hash-2", AdminSession{AdminID: 1, Role: "admin"}, time.Minute))

	mr.FastForward(2 * time.Minute)

	_, found, err := store.Get(ctx, "hash-2")
	require.NoError(t, err)
	require.False(t, found, "сессия обязана исчезнуть по TTL")
}

func TestAdminSessionTouchExtendsTTL(t *testing.T) {
	mr, store := newTestAdminSessionStore(t)
	defer mr.Close()

	ctx := context.Background()
	require.NoError(t, store.Save(ctx, "hash-3", AdminSession{AdminID: 1, Role: "admin"}, time.Minute))

	mr.FastForward(50 * time.Second)
	require.NoError(t, store.Touch(ctx, "hash-3", time.Minute))
	mr.FastForward(50 * time.Second)

	_, found, err := store.Get(ctx, "hash-3")
	require.NoError(t, err)
	require.True(t, found, "Touch обязан продлить простаивающую сессию")
}

func TestAdminSessionTouchMissingIsNoError(t *testing.T) {
	mr, store := newTestAdminSessionStore(t)
	defer mr.Close()

	require.NoError(t, store.Touch(context.Background(), "gone", time.Minute))
}

func TestAdminSessionDelete(t *testing.T) {
	mr, store := newTestAdminSessionStore(t)
	defer mr.Close()

	ctx := context.Background()
	require.NoError(t, store.Save(ctx, "hash-4", AdminSession{AdminID: 1, Role: "admin"}, time.Hour))
	require.NoError(t, store.Delete(ctx, "hash-4"))

	_, found, err := store.Get(ctx, "hash-4")
	require.NoError(t, err)
	require.False(t, found)
}

func TestAdminSessionGetCorruptValueIsDeleted(t *testing.T) {
	mr, store := newTestAdminSessionStore(t)
	defer mr.Close()

	require.NoError(t, mr.Set(AdminSessionKey("h"), "{not json"))

	_, found, err := store.Get(context.Background(), "h")
	require.NoError(t, err)
	require.False(t, found)
	require.False(t, mr.Exists(AdminSessionKey("h")), "битый ключ должен быть удалён")
}
