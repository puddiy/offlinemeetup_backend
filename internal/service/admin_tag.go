package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo"
	"github.com/uptrace/bun"
)

// AdminTagRepository — то, что справочник тегов требует от хранилища.
// Удовлетворяется *repo.TagRepo.
type AdminTagRepository interface {
	RunInTx(ctx context.Context, fn func(tx bun.Tx) error) error
	ListWithUsage(ctx context.Context) ([]repo.TagUsage, error)
	CreateTx(ctx context.Context, tx bun.IDB, name string) (*domain.Tag, error)
	RenameTx(ctx context.Context, tx bun.IDB, id int64, name string) (string, error)
	SetHiddenTx(ctx context.Context, tx bun.IDB, id int64, hidden bool) error
}

// adminTagCache — узкий срез TagCache: каталог /v1/tags кэшируется целиком,
// и любая мутация справочника обязана его сбросить.
type adminTagCache interface {
	InvalidateTags(ctx context.Context) error
}

const (
	minTagNameLen = 2
	maxTagNameLen = 40
)

// AdminTagService — справочник тегов для админки: список со счётчиками,
// создание, переименование, скрытие.
//
// Каждая мутация — одна транзакция с записью журнала; каталог /v1/tags
// сбрасывается после коммита. Снапшоты митапов и профилей с переименованным
// тегом НЕ сбрасываются и показывают старое имя до своего TTL — осознанный
// компромисс: переименование редкое и безвредное, а обход всех митапов с
// тегом ради мгновенности не окупается.
type AdminTagService struct {
	repo  AdminTagRepository
	audit AuditRecorder
	cache adminTagCache
	log   *slog.Logger
}

func NewAdminTagService(r AdminTagRepository, audit AuditRecorder, c adminTagCache, log *slog.Logger) *AdminTagService {
	return &AdminTagService{repo: r, audit: audit, cache: c, log: log}
}

// normalizeTagName схлопывает пробелы и проверяет длину в символах (не
// байтах — теги кириллические). «  Настольные   игры » и «Настольные игры»
// обязаны быть одним тегом, иначе индекс lower(name) их не поймает.
func normalizeTagName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if n := utf8.RuneCountInString(name); n < minTagNameLen || n > maxTagNameLen {
		return "", ErrInvalidInput
	}
	return name, nil
}

// List отдаёт все теги со счётчиками использования.
func (s *AdminTagService) List(ctx context.Context) ([]dto.AdminTagRow, error) {
	usage, err := s.repo.ListWithUsage(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	rows := make([]dto.AdminTagRow, 0, len(usage))
	for _, u := range usage {
		rows = append(rows, dto.AdminTagRow{ID: u.ID, Name: u.Name, IsHidden: u.IsHidden, Meetups: u.Meetups, Users: u.Users})
	}
	return rows, nil
}

// Create добавляет тег в каталог.
func (s *AdminTagService) Create(ctx context.Context, actorID int64, name, ip string) error {
	name, err := normalizeTagName(name)
	if err != nil {
		return err
	}
	err = s.repo.RunInTx(ctx, func(tx bun.Tx) error {
		tag, err := s.repo.CreateTx(ctx, tx, name)
		if err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, tagAuditEvent(actorID, AuditActionTagCreate, tag.ID, ip,
			map[string]any{"name": name}))
	})
	if err != nil {
		return mapAdminTagError(err, "create tag")
	}
	s.invalidateCatalog(ctx)
	return nil
}

// Rename переименовывает тег; прежнее и новое имя уходят в журнал.
func (s *AdminTagService) Rename(ctx context.Context, actorID, tagID int64, name, ip string) error {
	name, err := normalizeTagName(name)
	if err != nil {
		return err
	}
	err = s.repo.RunInTx(ctx, func(tx bun.Tx) error {
		old, err := s.repo.RenameTx(ctx, tx, tagID, name)
		if err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, tagAuditEvent(actorID, AuditActionTagRename, tagID, ip,
			map[string]any{"from": old, "to": name}))
	})
	if err != nil {
		return mapAdminTagError(err, "rename tag")
	}
	s.invalidateCatalog(ctx)
	return nil
}

// SetHidden скрывает тег (hidden=true) или возвращает его в каталог.
func (s *AdminTagService) SetHidden(ctx context.Context, actorID, tagID int64, hidden bool, ip string) error {
	action := AuditActionTagUnhide
	if hidden {
		action = AuditActionTagHide
	}
	err := s.repo.RunInTx(ctx, func(tx bun.Tx) error {
		if err := s.repo.SetHiddenTx(ctx, tx, tagID, hidden); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, tagAuditEvent(actorID, action, tagID, ip, nil))
	})
	if err != nil {
		return mapAdminTagError(err, "set tag hidden")
	}
	s.invalidateCatalog(ctx)
	return nil
}

func tagAuditEvent(actorID int64, action string, tagID int64, ip string, details map[string]any) AuditEvent {
	return AuditEvent{
		AdminID:    actorID,
		Action:     action,
		TargetType: "tag",
		TargetID:   strconv.FormatInt(tagID, 10),
		IP:         ip,
		Details:    details,
	}
}

// invalidateCatalog сбрасывает кэш /v1/tags. Мутация уже закоммичена,
// поэтому сбой только логируется: каталог догонит БД по TTL.
func (s *AdminTagService) invalidateCatalog(ctx context.Context) {
	if err := s.cache.InvalidateTags(ctx); err != nil {
		s.log.Error("invalidating tag catalog", slog.Any("error", err))
	}
}

func mapAdminTagError(err error, op string) error {
	switch {
	case errors.Is(err, repo.ErrTagNameTaken):
		return ErrAlreadyExists
	case errors.Is(err, repo.ErrTagNotFound):
		return ErrNotFound
	default:
		return fmt.Errorf("%s: %w", op, err)
	}
}
