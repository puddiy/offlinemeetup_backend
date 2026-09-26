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

func (r *MeetupRepo) Update(ctx context.Context, meetup *domain.Meetup, newTagIDs []int64) error {
	return r.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// Новая обложка должна принадлежать создателю и быть изображением.
		if meetup.CoverFileID.Valid {
			if err := imageFileOwnedBy(ctx, tx, meetup.CoverFileID.UUID, meetup.CreatorID); err != nil {
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

		_, err = tx.NewDelete().Model((*domain.MeetupTag)(nil)).Where("meetup_id = ?", meetup.ID).Exec(ctx)
		if err != nil {
			return err
		}

		if len(newTagIDs) > 0 {
			meetupTags := make([]domain.MeetupTag, len(newTagIDs))
			for i, tagID := range newTagIDs {
				meetupTags[i] = domain.MeetupTag{
					MeetupID: meetup.ID,
					TagID:    tagID,
				}
			}
			_, err := tx.NewInsert().Model(&meetupTags).Exec(ctx)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *MeetupRepo) Delete(ctx context.Context, id int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.NewUpdate().
		Model((*domain.Meetup)(nil)).
		Set("status = ?", "cancelled").
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return err
	}

	_, err = tx.NewUpdate().
		Model((*domain.Chat)(nil)).
		Set("is_read_only = ?", true).
		Where("meetup_id = ?", id).
		Exec(ctx)
	if err != nil {
		return err
	}

	return tx.Commit()
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
