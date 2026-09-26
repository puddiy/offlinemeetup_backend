package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/uptrace/bun"
)

// Repo-level sentinel errors. The service layer translates these into its own
// domain sentinels (service.ErrChatReadOnly / service.ErrForbidden) at the
// boundary, so the repo stays unaware of the service package while callers can
// still branch on the cause with errors.Is.
var (
	// ErrChatReadOnly means a non-system message was sent to a read-only chat.
	ErrChatReadOnly = errors.New("chat is read-only")
	// ErrNotChatMember means the sender is not a participant of the chat.
	ErrNotChatMember = errors.New("user is not a member of this chat")
	// ErrMessageNotFound means the target message does not exist or is deleted.
	ErrMessageNotFound = errors.New("message not found")
	// ErrNotMessageAuthor means the actor is not the author of the message.
	ErrNotMessageAuthor = errors.New("user is not the author of this message")
)

type ChatRepo struct {
	db *bun.DB
}

func NewChatRepo(db *bun.DB) *ChatRepo {
	return &ChatRepo{db: db}
}

func (r *ChatRepo) CreateGroupChat(ctx context.Context, tx bun.IDB, chat *domain.Chat) error {
	_, err := tx.NewInsert().Model(chat).Returning("id").Exec(ctx)
	return err
}

func (r *ChatRepo) AddParticipant(ctx context.Context, tx bun.IDB, chatParticipant *domain.ChatParticipant) error {
	_, err := tx.NewInsert().Model(chatParticipant).Ignore().Exec(ctx)
	return err
}

func (r *ChatRepo) RemoveParticipant(ctx context.Context, tx bun.IDB, chatID, userID int64) error {
	_, err := tx.NewDelete().
		Table("chat_participants").
		Where("chat_id = ? AND user_id = ?", chatID, userID).
		Exec(ctx)
	return err
}

func (r *ChatRepo) GetChatByMeetupID(ctx context.Context, tx bun.IDB, meetupID int64) (*domain.Chat, error) {
	var chat domain.Chat
	err := tx.NewSelect().Model(&chat).Where("meetup_id = ?", meetupID).Scan(ctx)
	if err != nil {
		return nil, err
	}

	return &chat, nil
}

func (r *ChatRepo) GetUserChats(ctx context.Context, userID int64) ([]domain.Chat, error) {
	var chats []domain.Chat
	err := r.db.NewSelect().Model(&chats).
		ColumnExpr("chat.*").
		ColumnExpr("(SELECT content FROM messages m2 WHERE m2.chat_id = chat.id AND m2.deleted_at IS NULL ORDER BY m2.created_at DESC LIMIT 1) AS last_message_text").
		ColumnExpr("(SELECT COUNT(id) FROM messages m2 WHERE m2.chat_id = chat.id AND m2.id > cp.last_read_message_id AND m2.deleted_at IS NULL) AS unread_count").
		Relation("Meetup").
		Relation("Meetup.Creator").
		Relation("Meetup.Creator.Profile").
		Relation("Meetup.Creator.Profile.AvatarFile").
		Relation("Meetup.Tags").
		Relation("Meetup.CoverFile").
		Join("JOIN chat_participants cp ON cp.chat_id = chat.id").
		Where("cp.user_id = ?", userID).
		OrderExpr("GREATEST(COALESCE((SELECT MAX(created_at) FROM messages WHERE chat_id = chat.id), '1970-01-01'::timestamp), chat.created_at) DESC").
		Scan(ctx)

	if err != nil {
		return nil, fmt.Errorf("getting user's chats failed: %w", err)
	}

	return chats, nil
}

// messageByID loads a single message with the relations the DTO mapper needs
// (sender + avatar, and a one-level reply preview). Returns ErrMessageNotFound
// when the row is absent.
func (r *ChatRepo) messageByID(ctx context.Context, id int64) (*domain.Message, error) {
	var m domain.Message
	err := r.db.NewSelect().Model(&m).
		Relation("Sender").
		Relation("Sender.Profile").
		Relation("Sender.Profile.AvatarFile").
		Relation("ReplyTo").
		Relation("ReplyTo.Sender").
		Relation("ReplyTo.Sender.Profile").
		Relation("File").
		Where("message.id = ?", id).
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMessageNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("loading message %d: %w", id, err)
	}
	return &m, nil
}

func (r *ChatRepo) GetMessages(ctx context.Context, userID, chatID, cursor int64, limit int) ([]domain.Message, error) {
	var messages []domain.Message

	query := r.db.NewSelect().Model(&messages).
		Relation("Sender").
		Relation("Sender.Profile").
		Relation("Sender.Profile.AvatarFile").
		Relation("ReplyTo").
		Relation("ReplyTo.Sender").
		Relation("ReplyTo.Sender.Profile").
		Relation("File").
		Where("message.chat_id = ? AND EXISTS (SELECT 1 FROM chat_participants WHERE chat_id = ? AND user_id = ?)", chatID, chatID, userID)

	if cursor > 0 {
		query = query.Where("message.id < ?", cursor)
	}

	err := query.Order("message.id DESC").Limit(limit).Scan(ctx)

	if err != nil {
		return nil, fmt.Errorf("getting messages failed: %w", err)
	}

	return messages, nil
}

// SaveMessage persists a message and returns it, the chat's participant IDs for
// broadcast, and whether it was newly created. When msg.RequestID is set it is
// idempotent: a repeated (sender_id, request_id) does not insert a duplicate but
// returns the already-stored message with created=false. The de-dup is atomic
// (INSERT ... ON CONFLICT DO NOTHING against a unique index), not a check-then-
// act, so two concurrent retries can't both slip through.
func (r *ChatRepo) SaveMessage(ctx context.Context, msg *domain.Message) (*domain.Message, []int64, bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, false, err
	}
	// После успешного Commit Rollback вернёт sql.ErrTxDone — это штатно,
	// поэтому ошибку отката не проверяем (так же в EditMessage/DeleteMessage).
	defer func() { _ = tx.Rollback() }()

	// Идемпотентность: если сообщение с этим ключом уже создано — возвращаем его,
	// НЕ перепроверяя read-only/членство заново (они выполнялись при создании) и
	// не создавая дубль. Так ретрай после смены состояния чата (стал read-only,
	// вышел из чата) всё равно вернёт оригинал, а не ошибку. Коммитнутую строку
	// видно без гонки; гонку конкурентных НОВЫХ вставок ловит ON CONFLICT ниже.
	if msg.RequestID != nil {
		var existingID int64
		err := tx.NewSelect().Table("messages").Column("id").
			Where("chat_id = ? AND sender_id = ? AND request_id = ?", msg.ChatID, msg.SenderID, *msg.RequestID).
			Scan(ctx, &existingID)
		if err == nil {
			targetIDs, err := participantIDs(ctx, tx, msg.ChatID)
			if err != nil {
				return nil, nil, false, err
			}
			if err := tx.Commit(); err != nil {
				return nil, nil, false, fmt.Errorf("failed to commit transaction: %w", err)
			}
			loaded, err := r.messageByID(ctx, existingID)
			if err != nil {
				return nil, nil, false, fmt.Errorf("failed to load message after save: %w", err)
			}
			return loaded, targetIDs, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, nil, false, fmt.Errorf("checking idempotent message: %w", err)
		}
	}

	var isReadOnly bool
	err = tx.NewSelect().
		Table("chats").
		Column("is_read_only").
		Where("id = ?", msg.ChatID).
		Scan(ctx, &isReadOnly)
	if err != nil {
		return nil, nil, false, fmt.Errorf("failed to check chat status: %w", err)
	}
	if isReadOnly && msg.SenderID != 0 { // Allow system messages (SenderID=0)
		return nil, nil, false, ErrChatReadOnly
	}

	exists, err := tx.NewSelect().
		TableExpr("chat_participants").
		Where("chat_id = ? AND user_id = ?", msg.ChatID, msg.SenderID).
		Exists(ctx)
	if err != nil {
		return nil, nil, false, fmt.Errorf("checking if chat_participants exists failed: %w", err)
	}
	if !exists {
		return nil, nil, false, ErrNotChatMember
	}

	// Вложение должно принадлежать отправителю — нельзя приложить чужой файл по
	// его id. Проверяем в той же транзакции, что и членство.
	if msg.FileID.Valid {
		owned, err := fileOwnedBy(ctx, tx, msg.FileID.UUID, msg.SenderID)
		if err != nil {
			return nil, nil, false, err
		}
		if !owned {
			return nil, nil, false, ErrFileNotOwned
		}
	}

	// Ответ должен ссылаться на сообщение ИЗ ТОГО ЖЕ чата. Иначе через
	// reply_to_message_id можно вытащить превью чужого приватного сообщения:
	// messageByID грузит связь ReplyTo без фильтра по чату/членству, и превью
	// (ReplyTo.Content) ушло бы участникам этого чата. Проверяем в той же
	// транзакции; чужой/несуществующий id неотличим — оба дают "not found".
	if msg.ReplyToMessageID != nil {
		var parentChatID int64
		err = tx.NewSelect().
			Table("messages").
			Column("chat_id").
			Where("id = ?", *msg.ReplyToMessageID).
			Scan(ctx, &parentChatID)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && parentChatID != msg.ChatID) {
			return nil, nil, false, ErrMessageNotFound
		}
		if err != nil {
			return nil, nil, false, fmt.Errorf("checking reply target: %w", err)
		}
	}

	created := true
	if msg.RequestID != nil {
		// Pre-SELECT выше не нашёл строку, но два конкурентных НОВЫХ ретрая могли
		// дойти сюда одновременно. ON CONFLICT DO NOTHING атомарен на уровне БД:
		// один вставит, второй получит 0 строк. Ключ включает chat_id, поэтому
		// тот же request_id в другом чате — это отдельное сообщение, не дубль.
		res, err := tx.NewInsert().Model(msg).
			On("CONFLICT (chat_id, sender_id, request_id) WHERE request_id IS NOT NULL DO NOTHING").
			Exec(ctx)
		if err != nil {
			return nil, nil, false, fmt.Errorf("failed to insert message: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, nil, false, err
		}
		created = n > 0

		// После INSERT ... ON CONFLICT строка гарантированно существует (вставили
		// мы или конкурент). Достаём её id по уникальному ключу в любом случае.
		var existingID int64
		if err := tx.NewSelect().Table("messages").Column("id").
			Where("chat_id = ? AND sender_id = ? AND request_id = ?", msg.ChatID, msg.SenderID, *msg.RequestID).
			Scan(ctx, &existingID); err != nil {
			return nil, nil, false, fmt.Errorf("loading idempotent message id: %w", err)
		}
		msg.ID = existingID
	} else {
		if _, err = tx.NewInsert().Model(msg).Returning("id, created_at").Exec(ctx); err != nil {
			return nil, nil, false, fmt.Errorf("failed to insert message: %w", err)
		}
	}

	// Счётчик прочитанного двигаем только для НОВОГО сообщения; на дубле это уже
	// сделано при первой отправке.
	if created {
		_, err = tx.NewUpdate().Table("chat_participants").Set("last_read_message_id = ?", msg.ID).
			Where("chat_id = ? AND user_id = ?", msg.ChatID, msg.SenderID).
			Exec(ctx)
		if err != nil {
			return nil, nil, false, fmt.Errorf("failed to update sender read status: %w", err)
		}
	}

	targetIDs, err := participantIDs(ctx, tx, msg.ChatID)
	if err != nil {
		return nil, nil, false, err
	}

	if err := tx.Commit(); err != nil {
		return nil, nil, false, fmt.Errorf("failed to commit transaction: %w", err)
	}

	loaded, err := r.messageByID(ctx, msg.ID)
	if err != nil {
		return nil, nil, false, fmt.Errorf("failed to load message after save: %w", err)
	}

	return loaded, targetIDs, created, nil
}

// EditMessage updates a message's content (and stamps edited_at) only if the
// actor is its author and it is not deleted. The row is locked FOR UPDATE so the
// author/deleted checks and the update are atomic. Returns the reloaded message
// and the chat's participant IDs for broadcast.
func (r *ChatRepo) EditMessage(ctx context.Context, chatID, msgID, editorID int64, content string) (*domain.Message, []int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	existing, err := lockMessage(ctx, tx, msgID)
	if err != nil {
		return nil, nil, err
	}
	// Сообщение должно принадлежать чату из URL — иначе для этого чата его "нет"
	// (не раскрываем, что id существует в другом чате).
	if existing.ChatID != chatID {
		return nil, nil, ErrMessageNotFound
	}
	if existing.SenderID != editorID {
		return nil, nil, ErrNotMessageAuthor
	}

	_, err = tx.NewUpdate().
		Table("messages").
		Set("content = ?", content).
		Set("edited_at = now()").
		Where("id = ?", msgID).
		Exec(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to update message: %w", err)
	}

	targetIDs, err := participantIDs(ctx, tx, existing.ChatID)
	if err != nil {
		return nil, nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	loaded, err := r.messageByID(ctx, msgID)
	if err != nil {
		return nil, nil, err
	}
	return loaded, targetIDs, nil
}

// DeleteMessage soft-deletes a message (stamps deleted_at) if the actor is its
// author and it is not already deleted. Returns the chat ID and participant IDs
// for broadcast.
func (r *ChatRepo) DeleteMessage(ctx context.Context, chatID, msgID, editorID int64) (int64, []int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	existing, err := lockMessage(ctx, tx, msgID)
	if err != nil {
		return 0, nil, err
	}
	// Сообщение должно принадлежать чату из URL (см. EditMessage).
	if existing.ChatID != chatID {
		return 0, nil, ErrMessageNotFound
	}
	if existing.SenderID != editorID {
		return 0, nil, ErrNotMessageAuthor
	}

	_, err = tx.NewUpdate().
		Table("messages").
		Set("deleted_at = now()").
		Where("id = ?", msgID).
		Exec(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to delete message: %w", err)
	}

	targetIDs, err := participantIDs(ctx, tx, existing.ChatID)
	if err != nil {
		return 0, nil, err
	}

	if err := tx.Commit(); err != nil {
		return 0, nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return existing.ChatID, targetIDs, nil
}

// lockMessage selects a live (not soft-deleted) message FOR UPDATE, mapping a
// missing or deleted row to ErrMessageNotFound.
func lockMessage(ctx context.Context, tx bun.IDB, msgID int64) (*domain.Message, error) {
	var m domain.Message
	err := tx.NewSelect().
		Model(&m).
		Column("id", "chat_id", "sender_id", "deleted_at").
		Where("id = ?", msgID).
		For("UPDATE").
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMessageNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("locking message %d: %w", msgID, err)
	}
	if m.DeletedAt != nil {
		return nil, ErrMessageNotFound
	}
	return &m, nil
}

// participantIDs returns the user IDs of a chat's members within a transaction.
func participantIDs(ctx context.Context, tx bun.IDB, chatID int64) ([]int64, error) {
	var ids []int64
	err := tx.NewSelect().
		TableExpr("chat_participants").
		Column("user_id").
		Where("chat_id = ?", chatID).
		Scan(ctx, &ids)
	if err != nil {
		return nil, fmt.Errorf("failed to get chat participants: %w", err)
	}
	return ids, nil
}

func (r *ChatRepo) MarkAsRead(ctx context.Context, chatID, userID, lastReadMessageID int64) error {
	// Без этой проверки UPDATE для не-участника был бы тихим no-op (0 строк),
	// а вызывающий слой всё равно разослал бы событие участникам чужого чата.
	// Проверяем членство явно, как в SaveMessage.
	exists, err := r.db.NewSelect().
		TableExpr("chat_participants").
		Where("chat_id = ? AND user_id = ?", chatID, userID).
		Exists(ctx)
	if err != nil {
		return fmt.Errorf("checking chat membership failed: %w", err)
	}
	if !exists {
		return ErrNotChatMember
	}

	_, err = r.db.NewUpdate().
		Table("chat_participants").
		Set("last_read_message_id = ?", lastReadMessageID).
		Where("chat_id = ? AND user_id = ?", chatID, userID).
		Where("last_read_message_id < ?", lastReadMessageID).
		Exec(ctx)

	if err != nil {
		return fmt.Errorf("failed to mark message as read: %w", err)
	}
	return nil
}

func (r *ChatRepo) GetChatParticipantIDs(ctx context.Context, chatID int64) ([]int64, error) {
	var targetIDs []int64
	err := r.db.NewSelect().
		TableExpr("chat_participants").
		Column("user_id").
		Where("chat_id = ?", chatID).
		Scan(ctx, &targetIDs)

	return targetIDs, err
}

// GetCoChatUserIDs returns the distinct IDs of users who share at least one chat
// with userID (excluding userID itself) — the audience for that user's presence
// changes. One self-join keeps it a single round-trip instead of N+1.
func (r *ChatRepo) GetCoChatUserIDs(ctx context.Context, userID int64) ([]int64, error) {
	var ids []int64
	err := r.db.NewSelect().
		ColumnExpr("DISTINCT cp2.user_id").
		TableExpr("chat_participants AS cp1").
		Join("JOIN chat_participants AS cp2 ON cp2.chat_id = cp1.chat_id").
		Where("cp1.user_id = ?", userID).
		Where("cp2.user_id <> ?", userID).
		Scan(ctx, &ids)
	if err != nil {
		return nil, fmt.Errorf("getting co-chat user ids failed: %w", err)
	}
	return ids, nil
}
