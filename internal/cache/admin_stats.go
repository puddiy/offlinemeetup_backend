package cache

import (
	"context"
	"time"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
)

// AdminStatsCache кеширует тяжёлую часть дашборда админки. Инвалидации нет —
// только TTL: несколько минут задержки на счётчиках никому не мешают, а
// сбрасывать снапшот на каждую регистрацию и сообщение значило бы не
// кешировать вовсе.
type AdminStatsCache struct {
	cache   Cache
	metrics Metrics
	ttl     time.Duration
}

// NewAdminStatsCache создаёт AdminStatsCache поверх любого Cache.
func NewAdminStatsCache(c Cache, m Metrics, ttl time.Duration) *AdminStatsCache {
	return &AdminStatsCache{cache: c, metrics: m, ttl: ttl}
}

// Snapshot возвращает снапшот из кеша или строит его через load.
func (c *AdminStatsCache) Snapshot(ctx context.Context, load func() (dto.DashboardSnapshot, error)) (dto.DashboardSnapshot, error) {
	return Load(ctx, c.cache, c.metrics, "admin_stats", AdminStatsKey(), c.ttl, load)
}
