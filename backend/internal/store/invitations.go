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
