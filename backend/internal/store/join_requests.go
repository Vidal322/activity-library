package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type JoinRequest struct {
	ID          string     `db:"id"`
	RequesterID string     `db:"requester"`
	Message     string     `db:"message"`
	State       string     `db:"state"`
	DecidedBy   *string    `db:"decided_by"`
	CreatedAt   time.Time  `db:"created_at"`
	DecidedAt   *time.Time `db:"decided_at"`
}

const createJoinRequestQuery = `
	INSERT INTO join_requests (requester, message)
	VALUES (@requester, @message)
	RETURNING id, requester, message, state, decided_by, created_at, decided_at
`

func CreateJoinRequest(
	ctx context.Context,
	pool *pgxpool.Pool,
	requesterID string,
	message string,
) (JoinRequest, error) {
	rows, err := pool.Query(ctx, createJoinRequestQuery, pgx.NamedArgs{
		"requester": requesterID,
		"message":   message,
	})
	if err != nil {
		return JoinRequest{}, classify(err)
	}

	joinReq, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[JoinRequest])
	if err != nil {
		return JoinRequest{}, classify(err)
	}

	return joinReq, nil
}
