package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/driver/pgdriver"
)

// Sentinels for the unique indexes the email/password registration writes
// against. The unique index is the only real arbiter of username/email
// ownership (ADR-9) — a pre-flight SELECT can always lose the race — so these
// translate a Postgres 23505 into something the service can map to a domain
// error. Repo owns its own sentinels; the service translates them.
var (
	ErrUsernameTaken = errors.New("username already taken")
	ErrEmailTaken    = errors.New("email already registered")
)

// uniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505) and returns the offending constraint/index name.
func uniqueViolation(err error) (string, bool) {
	var pgErr pgdriver.Error
	if !errors.As(err, &pgErr) {
		return "", false
	}
	if pgErr.Field('C') != "23505" {
		return "", false
	}
	// 'n' is the constraint name; some servers/pool proxies omit it, so fall
	// back to the human-readable message which also names the index.
	name := pgErr.Field('n')
	if name == "" {
		name = pgErr.Field('M')
	}
	return name, true
}

// mapUniqueViolation turns a 23505 on the username/email unique indexes into
// the matching repo sentinel; anything else is returned untouched.
func mapUniqueViolation(err error) error {
	name, ok := uniqueViolation(err)
	if !ok {
		return err
	}
	switch {
	case strings.Contains(name, "username"):
		return ErrUsernameTaken
	case strings.Contains(name, "email"):
		return ErrEmailTaken
	default:
		return err
	}
}

type UserRepo struct {
	db *bun.DB
}

func NewUserRepo(db *bun.DB) *UserRepo {
	return &UserRepo{db: db}
}

func (r *UserRepo) GetBySocialID(ctx context.Context, provider, socialID string) (*domain.User, error) {
	var socialAccount domain.SocialAccount

	err := r.db.NewSelect().Model(&socialAccount).Relation("User").Where("provider = ?", provider).Where("social_id = ?", socialID).Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	return socialAccount.User, nil
}

func (r *UserRepo) CreateUserWithSocial(ctx context.Context, user *domain.User, provider, socialID string, profile *domain.Profile) (*domain.User, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, err
	}

	defer func() { _ = tx.Rollback() }()

	_, err = tx.NewInsert().Model(user).Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("error inserting user: %w", err)
	}

	social := &domain.SocialAccount{
		UserID:   user.ID,
		Provider: provider,
		SocialID: socialID,
	}

	_, err = tx.NewInsert().Model(social).Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("error creating social account: %w", err)
	}

	profile.UserID = user.ID
	_, err = tx.NewInsert().Model(profile).Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("error creating profile: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return user, nil
}

// FindIDByEmail returns the id of the user owning an email, or ErrNotFound.
// Matched case-insensitively against the uq_users_email_lower index; the
// query lowers the argument itself so a caller that forgot to normalize
// (ADR-3) still gets the right answer.
func (r *UserRepo) FindIDByEmail(ctx context.Context, email string) (int64, error) {
	var id int64
	err := r.db.NewSelect().
		Model((*domain.User)(nil)).
		Column("id").
		Where("lower(email) = lower(?)", email).
		Limit(1).
		Scan(ctx, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("finding user by email: %w", err)
	}
	return id, nil
}

// FindIDByUsername returns the id of the user owning a username, or
// ErrNotFound. Matched case-insensitively against uq_profile_username_lower.
// This is only ever a soft/advisory check — the unique index inside the
// registration transaction is the authoritative one (ADR-9).
func (r *UserRepo) FindIDByUsername(ctx context.Context, username string) (int64, error) {
	var id int64
	err := r.db.NewSelect().
		Model((*domain.Profile)(nil)).
		Column("user_id").
		Where("lower(username) = lower(?)", username).
		Limit(1).
		Scan(ctx, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("finding user by username: %w", err)
	}
	return id, nil
}

// CreateUserWithPassword creates a brand-new password-registered account:
// users + profile + user_credentials rows in ONE transaction, so a crash can
// never leave a user without a profile or with an unusable (password-less)
// account. display_name is left NULL — a fresh registration has a username
// but no separate display name yet (ADR-5 semantics).
//
// A unique-index conflict on lower(username) (someone took the name between
// the caller's soft check and this insert) surfaces as ErrUsernameTaken; a
// conflict on the email surfaces as ErrEmailTaken.
func (r *UserRepo) CreateUserWithPassword(ctx context.Context, email, username, passwordHash string) (int64, error) {
	var userID int64

	err := r.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		user := &domain.User{
			Email:  email,
			Role:   "user",
			Status: domain.UserStatusActive,
		}
		if _, err := tx.NewInsert().Model(user).Exec(ctx); err != nil {
			return fmt.Errorf("inserting user: %w", mapUniqueViolation(err))
		}

		profile := &domain.Profile{
			UserID:   user.ID,
			Username: username,
		}
		if _, err := tx.NewInsert().Model(profile).Exec(ctx); err != nil {
			return fmt.Errorf("inserting profile: %w", mapUniqueViolation(err))
		}

		if err := upsertCredentials(ctx, tx, user.ID, passwordHash); err != nil {
			return err
		}

		userID = user.ID
		return nil
	})
	if err != nil {
		return 0, err
	}
	return userID, nil
}

// AttachPassword adds (or replaces) the password of an EXISTING account —
// ADR-7's path, where someone registers with an email that already has an
// account created via Google/Telegram. It deliberately does not touch the
// existing profile: that account already has a username, and silently
// renaming it on behalf of whoever ran `register` would let a confirmed-email
// caller rewrite an established identity.
func (r *UserRepo) AttachPassword(ctx context.Context, userID int64, passwordHash string) error {
	return r.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		exists, err := tx.NewSelect().
			Model((*domain.User)(nil)).
			Where("id = ?", userID).
			Exists(ctx)
		if err != nil {
			return fmt.Errorf("checking user exists: %w", err)
		}
		if !exists {
			return ErrNotFound
		}
		return upsertCredentials(ctx, tx, userID, passwordHash)
	})
}

// LinkSocialAccount attaches a social provider identity to an EXISTING user.
// Used when a social login resolves to an email that already has an account
// (created by a password registration, or by another provider): without this
// the login path would try to INSERT a second user row carrying the same
// email and hit the users-email unique index, locking the owner out of that
// login method for good.
//
// ON CONFLICT DO NOTHING makes it idempotent against two concurrent first
// logins on the same provider identity (both callers then read the same
// user).
func (r *UserRepo) LinkSocialAccount(ctx context.Context, userID int64, provider, socialID string) error {
	social := &domain.SocialAccount{
		UserID:   userID,
		Provider: provider,
		SocialID: socialID,
	}
	_, err := r.db.NewInsert().
		Model(social).
		On("CONFLICT (provider, social_id) DO NOTHING").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("linking social account: %w", err)
	}
	return nil
}

func (r *UserRepo) GetTagsByUserID(ctx context.Context, userID int64) ([]domain.Tag, error) {
	var tags []domain.Tag

	err := r.db.NewSelect().
		Model(&tags).
		Join("JOIN user_tags ut ON ut.tag_id = tag.id").
		Where("ut.user_id = ?", userID).
		Order("tag.name ASC").
		Scan(ctx)
	return tags, err
}

func (r *UserRepo) UpdateTags(ctx context.Context, userID int64, tagIDs []int64) error {
	return r.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		_, err := tx.NewDelete().Model((*domain.UserTag)(nil)).Where("user_id = ?", userID).Exec(ctx)
		if err != nil {
			return err
		}

		if len(tagIDs) == 0 {
			return nil
		}

		userTags := make([]domain.UserTag, len(tagIDs))
		for i, id := range tagIDs {
			userTags[i] = domain.UserTag{
				UserID: userID,
				TagID:  id,
			}
		}

		_, err = tx.NewInsert().Model(&userTags).Exec(ctx)
		return err
	})
}

func (r *UserRepo) GetByID(ctx context.Context, id int64) (*domain.User, error) {
	var user domain.User
	err := r.db.NewSelect().
		Model(&user).
		Relation("Tags").
		Where("id = ?", id).
		Scan(ctx)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &user, nil
}
