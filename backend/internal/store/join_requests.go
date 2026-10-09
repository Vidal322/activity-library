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

type PendingJoinRequest struct {
	JoinRequest
	RequesterName  string `db:"requester_name"`
	RequesterEmail string `db:"requester_email"`
}

const listPendingJoinRequestsQuery = `
	SELECT jr.id, jr.requester, jr.message, jr.state, jr.decided_by,
	       jr.created_at, jr.decided_at,
	       u.name AS requester_name, u.email AS requester_email
	FROM join_requests jr
	JOIN users u ON u.id = jr.requester
	WHERE jr.state = 'pending'
	ORDER BY jr.created_at, jr.id
`

func ListPendingJoinRequests(
	ctx context.Context,
	pool *pgxpool.Pool,
) ([]PendingJoinRequest, error) {
	rows, err := pool.Query(ctx, listPendingJoinRequestsQuery)
	if err != nil {
		return nil, classify(err)
	}

	reqs, err := pgx.CollectRows(rows, pgx.RowToStructByName[PendingJoinRequest])
	if err != nil {
		return nil, classify(err)
	}

	return reqs, nil
}
