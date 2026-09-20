package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// AdminSession — состояние залогиненного администратора. Role лежит здесь
// только для дешёвых проверок в middleware; источником правды остаётся строка
// admin_users, которую AdminAuthService перечитывает на каждом запросе.
type AdminSession struct {
	AdminID  int64     `json:"admin_id"`
	Role     string    `json:"role"`
	IssuedAt time.Time `json:"issued_at"`
}

// RedisAdminSessionStore хранит сессии админ-панели в Redis. Как и
// RedisAuthStore (и в отличие от RedisCache), ошибки Redis возвращаются
// вызывающему и обязаны провалить запрос: «молча не смогли прочитать
// сессию» не должно превращаться в «пропустили запрос».
type RedisAdminSessionStore struct {
	rdb *redis.Client
	log *slog.Logger
}

func NewRedisAdminSessionStore(rdb *redis.Client, log *slog.Logger) *RedisAdminSessionStore {
	return &RedisAdminSessionStore{rdb: rdb, log: log}
}

// Save кладёт сессию под ключ AdminSessionKey(tokenHash) с заданным TTL.
func (s *RedisAdminSessionStore) Save(ctx context.Context, tokenHash string, sess AdminSession, ttl time.Duration) error {
	payload, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("marshal admin session: %w", err)
	}
	if err := s.rdb.Set(ctx, AdminSessionKey(tokenHash), payload, ttl).Err(); err != nil {
		s.log.Error("admin session save failed", slog.Any("error", err))
		return fmt.Errorf("save admin session: %w", err)
	}
	return nil
}

// Get возвращает сессию. found=false — нет такой сессии: не заводилась,
// истекла или разлогинена; три случая намеренно неотличимы.
func (s *RedisAdminSessionStore) Get(ctx context.Context, tokenHash string) (AdminSession, bool, error) {
	raw, err := s.rdb.Get(ctx, AdminSessionKey(tokenHash)).Bytes()
	if errors.Is(err, redis.Nil) {
		return AdminSession{}, false, nil
	}
	if err != nil {
		s.log.Error("admin session get failed", slog.Any("error", err))
		return AdminSession{}, false, fmt.Errorf("get admin session: %w", err)
	}

	var sess AdminSession
	if err := json.Unmarshal(raw, &sess); err != nil {
		// Битое значение лечим как отсутствие сессии: пользователь просто
		// перелогинится. Возвращать 500 на мусор в кэше смысла нет.
		s.log.Error("admin session unmarshal failed", slog.Any("error", err))
		// Ключ удаляем, иначе мусор пролежит до TTL и будет ронять каждый
		// запрос по нему; best-effort — ответ «не найдено» от этого не зависит.
		if delErr := s.rdb.Del(ctx, AdminSessionKey(tokenHash)).Err(); delErr != nil {
			s.log.Error("admin session corrupt key delete failed", slog.Any("error", delErr))
		}
		return AdminSession{}, false, nil
	}
	return sess, true, nil
}

// Touch продлевает TTL живой сессии (скользящее окно простоя). Отсутствие
// ключа — не ошибка: сессия истекла между Get и Touch, и следующий Get это
// увидит сам.
func (s *RedisAdminSessionStore) Touch(ctx context.Context, tokenHash string, ttl time.Duration) error {
	if err := s.rdb.Expire(ctx, AdminSessionKey(tokenHash), ttl).Err(); err != nil {
		s.log.Error("admin session touch failed", slog.Any("error", err))
		return fmt.Errorf("touch admin session: %w", err)
	}
	return nil
}

// Delete разлогинивает сессию.
func (s *RedisAdminSessionStore) Delete(ctx context.Context, tokenHash string) error {
	if err := s.rdb.Del(ctx, AdminSessionKey(tokenHash)).Err(); err != nil {
		s.log.Error("admin session delete failed", slog.Any("error", err))
		return fmt.Errorf("delete admin session: %w", err)
	}
	return nil
}
