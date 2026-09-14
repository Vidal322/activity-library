package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Session struct {
	TokenHash string     `db:"token_hash" json:"-"`
	UserID    string     `db:"user_id"`
	ExpiresAt time.Time  `db:"expires_at"`
	CreatedAt time.Time  `db:"created_at"`
	RevokedAt *time.Time `db:"revoked_at"`
}

const createSessionQuery = `
	INSERT INTO sessions (token_hash, user_id, expires_at)
	VALUES ($1, $2, $3)
	RETURNING token_hash, user_id, expires_at, created_at, revoked_at
`

func CreateSession(
	ctx context.Context,
	pool *pgxpool.Pool,
	userID string,
	tokenHash string,
	expiresAt time.Time,
) (Session, error) {
	rows, err := pool.Query(ctx, createSessionQuery, tokenHash, userID, expiresAt)
	if err != nil {
		return Session{}, classify(err)
	}

	session, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[Session])
	if err != nil {
		return Session{}, classify(err)
	}

	return session, nil
}

const getSessionQuery = `
	SELECT token_hash, user_id, expires_at, created_at, revoked_at
	FROM sessions
	WHERE token_hash = $1
	  AND revoked_at IS NULL
	  AND expires_at > now()
`

func GetSession(ctx context.Context, pool *pgxpool.Pool, tokenHash string) (Session, error) {
	rows, err := pool.Query(ctx, getSessionQuery, tokenHash)
	if err != nil {
		return Session{}, classify(err)
	}

	session, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[Session])
	if err != nil {
		return Session{}, classify(err)
	}

	return session, nil
}

const deleteSessionQuery = `
	DELETE FROM sessions
	WHERE token_hash = $1
`

func DeleteSession(ctx context.Context, pool *pgxpool.Pool, tokenHash string) error {
	tag, err := pool.Exec(ctx, deleteSessionQuery, tokenHash)
	if err != nil {
		return classifyDelete(err)
	}

	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	return nil
}
