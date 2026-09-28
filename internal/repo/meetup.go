package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/uptrace/bun"
)

// MeetupQuery is the repo-owned search criteria for List. It is deliberately a
// plain struct with no json tags: how an HTTP client serializes a request is a
// transport concern, so the SQL layer must not depend on the wire DTO. The
// service maps dto.MeetupFilter into this at its boundary (keeping the arrow
// transport→service→repo, not repo→transport).
type MeetupQuery struct {
	Lat, Lng    float64
	Radius      int
	Limit       int
	Offset      int
	Tags        []int64
	OnlyMy      bool
	OnlyCreated bool
	ExcludeOwn  bool
	ShowPast    bool
}

// MeetupAuth is the minimal projection needed to authorize a mutation on a
// meetup, without hydrating the full creator/participants/tags graph GetByID
// loads. Join/Leave/Delete read only a few scalar columns, so one cheap indexed
// read replaces several relation round-trips (and a whole participant roster).
type MeetupAuth struct {
	CreatorID int64
	IsPublic  bool
	Status    string
	EndTime   time.Time
	IsMember  bool
}

type MeetupRepo struct {
	db       *bun.DB
	chatRepo *ChatRepo
}

func NewMeetupRepo(db *bun.DB, chatRepo *ChatRepo) *MeetupRepo {
	return &MeetupRepo{db: db, chatRepo: chatRepo}
}

func (r *MeetupRepo) Create(ctx context.Context, meetup *domain.Meetup, chat *domain.Chat, tagIDs []int64) (*domain.Meetup, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// participants_count ведёт триггер БД (trg_participants_count) на вставку
	// creator-участника ниже — руками не трогаем, иначе двойной счёт.

	// Обложка должна принадлежать создателю и быть изображением.
	if meetup.CoverFileID.Valid {
		if err := imageFileOwnedBy(ctx, tx, meetup.CoverFileID.UUID, meetup.CreatorID); err != nil {
			return nil, err
		}
	}

	// Теги проверяются до вставки митапа: скрытый или несуществующий тег —
	// это 400 клиенту, а не 500 на нарушении FK посреди транзакции.
	tagIDs = uniqueIDs(tagIDs)
	if err := checkMeetupTagsTx(ctx, tx, 0, tagIDs); err != nil {
		return nil, err
	}

	_, err = tx.NewInsert().Model(meetup).Value("location", "ST_GeomFromText(?, 4326)", meetup.Location.String()).Returning("id, created_at, invite_token").Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("meetup creation failed: %w", err)
	}

	chat.MeetupID = &meetup.ID

	creatorParticipant := &domain.Participant{
		MeetupID: meetup.ID,
		UserID:   meetup.CreatorID,
		Role:     "organizer",
		Status:   "approved",
	}

	_, err = tx.NewInsert().Model(creatorParticipant).Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("adding creator to participants failed: %w", err)
	}

	if len(tagIDs) > 0 {
		meetupTags := make([]domain.MeetupTag, len(tagIDs))
		for i, tagID := range tagIDs {
			meetupTags[i] = domain.MeetupTag{
				MeetupID: meetup.ID,
				TagID:    tagID,
			}
		}
		if _, err := tx.NewInsert().Model(&meetupTags).Exec(ctx); err != nil {
			return nil, err
		}
	}
	err = r.chatRepo.CreateGroupChat(ctx, tx, chat)
	if err != nil {
		return nil, fmt.Errorf("creating group chat failed: %w", err)
	}

	chatParticipant := &domain.ChatParticipant{
		ChatID: chat.ID,
		UserID: meetup.CreatorID,
	}

	err = r.chatRepo.AddParticipant(ctx, tx, chatParticipant)
	if err != nil {
		return nil, fmt.Errorf("adding participant to chat failed: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("transaction commit failed: %w", err)
	}

	return r.GetByID(ctx, meetup.ID, meetup.CreatorID)
}

func (r *MeetupRepo) GetByID(ctx context.Context, id int64, currentUserID int64) (*domain.Meetup, error) {
	var meetup domain.Meetup

	q := r.db.NewSelect().
		Model(&meetup).
		Column("meetup.*").
		Relation("Creator").
		Relation("Creator.Profile").
		Relation("Creator.Profile.AvatarFile").
		Relation("Participants").
		Relation("Participants.Profile").
		Relation("Participants.Profile.AvatarFile").
		Relation("Tags").
		Relation("CoverFile").
		Where("meetup.id = ?", id)

	if currentUserID != 0 {
		q.ColumnExpr("EXISTS (SELECT 1 FROM participants AS sub_p WHERE sub_p.meetup_id = ?TableAlias.id AND sub_p.user_id = ?) AS is_member", currentUserID)
	}

	err := q.Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	return &meetup, nil
}

// GetForAuth loads only the scalar columns (plus is_member) needed to authorize
// a mutation, avoiding GetByID's relation fan-out. Returns (nil, nil) when the
// meetup does not exist, mirroring GetByID so the service maps it to ErrNotFound.
func (r *MeetupRepo) GetForAuth(ctx context.Context, id, userID int64) (*MeetupAuth, error) {
	var a MeetupAuth
	err := r.db.NewSelect().
		TableExpr("meetups AS m").
		ColumnExpr("m.creator_id, m.is_public, m.status, m.end_time").
		ColumnExpr("EXISTS (SELECT 1 FROM participants p WHERE p.meetup_id = m.id AND p.user_id = ?) AS is_member", userID).
		Where("m.id = ?", id).
		Scan(ctx, &a.CreatorID, &a.IsPublic, &a.Status, &a.EndTime, &a.IsMember)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loading meetup %d for auth: %w", id, err)
	}
	return &a, nil
}

func (r *MeetupRepo) GetByInviteToken(ctx context.Context, token uuid.UUID, currentUserID int64) (*domain.Meetup, error) {
	var meetup domain.Meetup

	q := r.db.NewSelect().
		Model(&meetup).
		Column("meetup.*").
		Where("meetup.invite_token = ?", token)

	if currentUserID != 0 {
		q.ColumnExpr("EXISTS (SELECT 1 FROM participants AS sub_p WHERE sub_p.meetup_id = ?TableAlias.id AND sub_p.user_id = ?) AS is_member", currentUserID)
	}

	err := q.Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	return &meetup, nil
}

func (r *MeetupRepo) List(ctx context.Context, filter MeetupQuery, currentUserID int64) ([]domain.Meetup, error) {
	var meetups []domain.Meetup

	q := r.db.NewSelect().Model(&meetups)
	q.Column("meetup.*")
	q.Relation("Creator")
	q.Relation("Creator.Profile")
	q.Relation("Creator.Profile.AvatarFile")
	q.Relation("Tags")
	q.Relation("CoverFile")

	if filter.OnlyCreated && currentUserID != 0 {
		q.Where("meetup.creator_id = ?", currentUserID)
	} else if filter.OnlyMy && currentUserID != 0 {
		q.Join("JOIN participants AS p ON p.meetup_id = meetup.id")
		q.Where("p.user_id = ?", currentUserID)
	} else if filter.ExcludeOwn && currentUserID != 0 {
		q.Where("meetup.creator_id != ?", currentUserID)
	}

	if !filter.OnlyMy && !filter.OnlyCreated {
		if currentUserID != 0 {
			q.WhereGroup(" AND ", func(sq *bun.SelectQuery) *bun.SelectQuery {
				return sq.Where("meetup.is_public = ?", true).
					WhereOr("EXISTS (SELECT 1 FROM participants p2 WHERE p2.meetup_id = meetup.id AND p2.user_id = ?)", currentUserID)
			})
		} else {
			q.Where("meetup.is_public = ?", true)
		}
	}

	if filter.ShowPast {
		q.Where("meetup.end_time < ?", time.Now())
		q.Order("meetup.end_time DESC")
	} else {
		q.Where("meetup.end_time > ?", time.Now())
		q.Where("meetup.status = ?", "active")
		if filter.OnlyMy {
			q.Order("p.joined_at DESC")

		} else if filter.OnlyCreated {
			q.Order("meetup.id DESC")

		} else if filter.Lat == 0 {
			q.Order("meetup.start_time ASC")
		}
	}

	if filter.Lat != 0 && filter.Lng != 0 {
		if filter.Radius > 0 {
			q.Where("ST_DWithin(?TableAlias.location, ST_MakePoint(?, ?)::geography, ?)",
				filter.Lng, filter.Lat, filter.Radius)
		}
		q.ColumnExpr("ST_Distance(?TableAlias.location, ST_MakePoint(?, ?)::geography) AS distance_meters",
			filter.Lng, filter.Lat)

		q.Order("distance_meters ASC")
	} else {
		q.Order("start_time ASC")
	}

	if currentUserID != 0 {
		q.ColumnExpr("EXISTS (SELECT 1 FROM participants AS sub_p WHERE sub_p.meetup_id = ?TableAlias.id AND sub_p.user_id = ?) AS is_member", currentUserID)
	}

	if len(filter.Tags) > 0 {
		q.Where("EXISTS (SELECT 1 FROM meetup_tags mt WHERE mt.meetup_id = meetup.id AND mt.tag_id IN (?))", bun.In(filter.Tags))
	}

	if filter.Limit > 0 {
		q.Limit(filter.Limit)
	}
	if filter.Offset > 0 {
		q.Offset(filter.Offset)
	}

	err := q.Scan(ctx)
	return meetups, err
}

// Update сохраняет редактируемые поля митапа. tagIDs == nil — теги не
// трогать (PATCH без поля tags); указатель на пустой срез — снять все.
func (r *MeetupRepo) Update(ctx context.Context, meetup *domain.Meetup, tagIDs *[]int64) error {
	return r.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// Новая обложка должна принадлежать создателю и быть изображением.
		if meetup.CoverFileID.Valid {
			if err := imageFileOwnedBy(ctx, tx, meetup.CoverFileID.UUID, meetup.CreatorID); err != nil {
				return err
			}
		}

		var ids []int64
		if tagIDs != nil {
			ids = uniqueIDs(*tagIDs)
			// До удаления старых связей: иначе «скрытый тег уже стоит»
			// не распознается (см. checkMeetupTagsTx).
			if err := checkMeetupTagsTx(ctx, tx, meetup.ID, ids); err != nil {
				return err
			}
		}

		// Обновляем только редактируемые поля. Model(meetup) без ExcludeColumn
		// переписал бы ВСЕ колонки значениями из ранее прочитанного existing —
		// включая participants_count (гонка lost-update с параллельным Join/
		// Leave) и неизменяемые creator_id/created_at/status.
		_, err := tx.NewUpdate().Model(meetup).
			ExcludeColumn("participants_count", "status", "creator_id", "created_at").
			Value("location", "ST_GeomFromText(?, 4326)", meetup.Location.String()).
			WherePK().
			Exec(ctx)
		if err != nil {
			return err
		}

		if tagIDs == nil {
			return nil
		}

		if _, err := tx.NewDelete().Model((*domain.MeetupTag)(nil)).Where("meetup_id = ?", meetup.ID).Exec(ctx); err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		meetupTags := make([]domain.MeetupTag, len(ids))
		for i, tagID := range ids {
			meetupTags[i] = domain.MeetupTag{MeetupID: meetup.ID, TagID: tagID}
		}
		_, err = tx.NewInsert().Model(&meetupTags).Exec(ctx)
		return err
	})
}

// Delete отменяет митап по решению создателя и возвращает user_id его
// участников: у их групповых чатов поменялся is_read_only, и вызывающий
// обязан сбросить им кэш списка чатов.
func (r *MeetupRepo) Delete(ctx context.Context, id int64) ([]int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := cancelMeetupsTx(ctx, tx, []int64{id}); err != nil {
		return nil, err
	}
	participants, err := meetupParticipantIDs(ctx, tx, []int64{id})
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return participants, nil
}

// ErrMeetupNotActive — митапа нет или он уже отменён: отменять нечего.
// Для модератора оба случая значат одно — «контента уже нет».
var ErrMeetupNotActive = errors.New("meetup not found or not active")

// CancelledMeetups — что отменила CancelActiveByCreatorTx. ParticipantIDs нужны
// вызывающему, чтобы после коммита сбросить кэш списков чатов: у чатов
// отменённых митапов поменялся is_read_only.
type CancelledMeetups struct {
	MeetupIDs      []int64
	ParticipantIDs []int64
}

// cancelMeetupsTx — ЕДИНСТВЕННОЕ определение «отменить митап» в кодовой базе:
// status='cancelled' плюс групповой чат только для чтения. Им пользуются
// удаление создателем, отмена модератором и каскад бана/удаления аккаунта —
// правило не должно разъехаться между ними.
//
// deleted_at (тег soft_delete) здесь сознательно НЕ ставится, как и раньше в
// Delete: отменённый митап остаётся видимым в истории участников.
func cancelMeetupsTx(ctx context.Context, idb bun.IDB, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := idb.NewUpdate().
		Model((*domain.Meetup)(nil)).
		Set("status = ?", "cancelled").
		Where("id IN (?)", bun.In(ids)).
		Exec(ctx); err != nil {
		return fmt.Errorf("cancel meetups: %w", err)
	}
	if _, err := idb.NewUpdate().
		Model((*domain.Chat)(nil)).
		Set("is_read_only = ?", true).
		Where("meetup_id IN (?)", bun.In(ids)).
		Exec(ctx); err != nil {
		return fmt.Errorf("freeze meetup chats: %w", err)
	}
	return nil
}

// meetupParticipantIDs — уникальные user_id участников перечисленных митапов.
func meetupParticipantIDs(ctx context.Context, idb bun.IDB, meetupIDs []int64) ([]int64, error) {
	var ids []int64
	err := idb.NewSelect().
		TableExpr("participants").
		ColumnExpr("DISTINCT user_id").
		Where("meetup_id IN (?)", bun.In(meetupIDs)).
		Scan(ctx, &ids)
	if err != nil {
		return nil, fmt.Errorf("meetup participants: %w", err)
	}
	return ids, nil
}

// CancelTx отменяет митап по решению модератора внутри транзакции
// вызывающего (там же пишется журнал). Возвращает user_id участников.
//
// Строка блокируется FOR UPDATE: два модератора, одновременно отменяющие
// один митап, не должны оба получить «успех» и оба записать журнал.
func (r *MeetupRepo) CancelTx(ctx context.Context, tx bun.IDB, meetupID int64) ([]int64, error) {
	var ids []int64
	err := tx.NewSelect().
		Model((*domain.Meetup)(nil)).
		Column("id").
		Where("id = ?", meetupID).
		Where("status = ?", "active").
		For("UPDATE").
		Scan(ctx, &ids)
	if err != nil {
		return nil, fmt.Errorf("lock meetup: %w", err)
	}
	if len(ids) == 0 {
		return nil, ErrMeetupNotActive
	}
	if err := cancelMeetupsTx(ctx, tx, ids); err != nil {
		return nil, err
	}
	return meetupParticipantIDs(ctx, tx, ids)
}

// CancelActiveByCreatorTx отменяет все митапы пользователя, которые ещё не
// закончились: будущие И идущие. Вызывается при бане и удалении аккаунта
// (решение продукта от 2026-09-26). Прошедшие не трогает. Разбан ничего не
// восстанавливает.
//
// Ноль митапов — не ошибка, а пустой результат.
func (r *MeetupRepo) CancelActiveByCreatorTx(ctx context.Context, tx bun.IDB, creatorID int64) (CancelledMeetups, error) {
	var ids []int64
	err := tx.NewSelect().
		Model((*domain.Meetup)(nil)).
		Column("id").
		Where("creator_id = ?", creatorID).
		Where("status = ?", "active").
		Where("end_time > ?", time.Now()).
		OrderExpr("id").
		For("UPDATE").
		Scan(ctx, &ids)
	if err != nil {
		return CancelledMeetups{}, fmt.Errorf("lock creator meetups: %w", err)
	}
	if len(ids) == 0 {
		return CancelledMeetups{}, nil
	}
	if err := cancelMeetupsTx(ctx, tx, ids); err != nil {
		return CancelledMeetups{}, err
	}
	participants, err := meetupParticipantIDs(ctx, tx, ids)
	if err != nil {
		return CancelledMeetups{}, err
	}
	return CancelledMeetups{MeetupIDs: ids, ParticipantIDs: participants}, nil
}

func (r *MeetupRepo) Join(ctx context.Context, meetupID, userID int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	participant := &domain.Participant{
		MeetupID: meetupID,
		UserID:   userID,
		Role:     "participant",
		Status:   "approved",
	}

	res, err := tx.NewInsert().
		Model(participant).
		On("CONFLICT (meetup_id, user_id) DO NOTHING").
		Exec(ctx)
	if err != nil {
		return err
	}

	inserted, err := res.RowsAffected()
	if err != nil {
		return err
	}
	// Пользователь уже участник (в т.ч. при гонке двух одновременных join) —
	// выходим без добавления в чат. ON CONFLICT DO NOTHING не вставляет строку,
	// поэтому триггер счётчика тоже не срабатывает (счётчик остаётся верным).
	if inserted == 0 {
		return tx.Commit()
	}

	chat, err := r.chatRepo.GetChatByMeetupID(ctx, tx, meetupID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return tx.Commit()
		}
		return err
	}

	chatParticipant := &domain.ChatParticipant{
		ChatID: chat.ID,
		UserID: userID,
	}

	err = r.chatRepo.AddParticipant(ctx, tx, chatParticipant)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (r *MeetupRepo) Leave(ctx context.Context, meetupID, userID int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.NewDelete().
		Table("participants").
		Where("meetup_id = ? AND user_id = ?", meetupID, userID).
		Exec(ctx)
	if err != nil {
		return err
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}

	if rows == 0 {
		return nil
	}

	// participants_count ведёт триггер БД на DELETE participants — руками не
	// трогаем. Симметрично Join: покидая митап, пользователь должен потерять
	// доступ к групповому чату — иначе остаётся в chat_participants и продолжает
	// читать/писать (проверки членства идут через EXISTS). ErrNoRows (у митапа
	// нет чата) — не ошибка.
	chat, err := r.chatRepo.GetChatByMeetupID(ctx, tx, meetupID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return tx.Commit()
		}
		return err
	}
	if err := r.chatRepo.RemoveParticipant(ctx, tx, chat.ID, userID); err != nil {
		return err
	}

	return tx.Commit()
}
