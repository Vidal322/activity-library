package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Invitation struct {
	ID         string     `db:"id"`
	Email      string     `db:"email"`
	TokenHash  string     `db:"token_hash"  json:"-"`
	InvitedBy  string     `db:"invited_by"`
	AcceptedBy *string    `db:"accepted_by"`
	CreatedAt  time.Time  `db:"created_at"`
	ExpiresAt  time.Time  `db:"expires_at"`
	AcceptedAt *time.Time `db:"accepted_at"`
	RevokedAt  *time.Time `db:"revoked_at"`
}

const createInvitationQuery = `
	INSERT INTO invitations (email, token_hash, invited_by, expires_at)
	VALUES (@email, @token_hash, @invited_by, @expires_at)
	RETURNING id, email, token_hash, invited_by, accepted_by,
	          created_at, expires_at, accepted_at, revoked_at
`

func CreateInvitation(
	ctx context.Context,
	pool *pgxpool.Pool,
	email string,
	invitedBy string,
	tokenHash string,
	expiresAt time.Time,
) (Invitation, error) {
	rows, err := pool.Query(ctx, createInvitationQuery, pgx.NamedArgs{
		"email":      email,
		"invited_by": invitedBy,
		"token_hash": tokenHash,
		"expires_at": expiresAt,
	})
	if err != nil {
		return Invitation{}, classify(err)
	}

	invitation, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[Invitation])
	if err != nil {
		return Invitation{}, classify(err)
	}
	return invitation, nil
}

const getInvitationByTokenHashQuery = `
	SELECT id, email, token_hash, invited_by, accepted_by,
	       created_at, expires_at, accepted_at, revoked_at
	FROM invitations
	WHERE token_hash = @token_hash
	  AND accepted_at IS NULL
	  AND revoked_at IS NULL
	  AND expires_at > now()
`

func GetInvitationByTokenHash(
	ctx context.Context,
	pool *pgxpool.Pool,
	tokenHash string,
) (Invitation, error) {
	rows, err := pool.Query(ctx, getInvitationByTokenHashQuery, pgx.NamedArgs{
		"token_hash": tokenHash,
	})
	if err != nil {
		return Invitation{}, classify(err)
	}

	invitation, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[Invitation])
	if err != nil {
		return Invitation{}, classify(err)
	}
	return invitation, nil
}

const lockOpenInvitationQuery = getInvitationByTokenHashQuery + `
	FOR UPDATE
`

const createInvitedUserQuery = `
	INSERT INTO users (name, email, pass_hash, img, role)
	VALUES (@name, @email, @pass_hash, @img, 'member')
	RETURNING id, name, email, pass_hash, img, role, active, created_at, updated_at
`

const markInvitationAcceptedQuery = `
	UPDATE invitations
	SET accepted_by = @accepted_by,
	    accepted_at = now()
	WHERE id = @id
`

// AcceptInvitation creates the account an open invitation was for and marks
// the invitation used, in one transaction
// An unknown, used, revoked or expired token is ErrNotFound. An email that
// already has an active account is ErrConflict on users_email_key, and the
// invitation stays open.
func AcceptInvitation(
	ctx context.Context,
	pool *pgxpool.Pool,
	tokenHash string,
	name string,
	passHash string,
	img *string,
) (User, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return User{}, classify(err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, lockOpenInvitationQuery, pgx.NamedArgs{
		"token_hash": tokenHash,
	})
	if err != nil {
		return User{}, classify(err)
	}

	invitation, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[Invitation])
	if err != nil {
		return User{}, classify(err)
	}

	rows, err = tx.Query(ctx, createInvitedUserQuery, pgx.NamedArgs{
		"name":      name,
		"email":     invitation.Email,
		"pass_hash": passHash,
		"img":       img,
	})
	if err != nil {
		return User{}, classify(err)
	}

	user, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[User])
	if err != nil {
		return User{}, classify(err)
	}

	_, err = tx.Exec(ctx, markInvitationAcceptedQuery, pgx.NamedArgs{
		"id":          invitation.ID,
		"accepted_by": user.ID,
	})
	if err != nil {
		return User{}, classify(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return User{}, classify(err)
	}

	return user, nil
}
