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

const decideJoinRequestQuery = `
	UPDATE join_requests
	SET state = @state,
	    decided_by = @decided_by,
	    decided_at = now()
	WHERE id = @id
  AND state = 'pending'
	RETURNING id, requester, message, state, decided_by, created_at, decided_at
`

func decideJoinRequest(
	ctx context.Context,
	q rowsQuerier,
	joinRequestID string,
	decidedBy string,
	state string,
) (JoinRequest, error) {
	rows, err := q.Query(ctx, decideJoinRequestQuery, pgx.NamedArgs{
		"id":         joinRequestID,
		"decided_by": decidedBy,
		"state":      state,
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

const promoteOutsiderQuery = `
	UPDATE users
	SET role = 'member'
	WHERE id = @id
	  AND role = 'outsider'
`

func AcceptJoinRequest(
	ctx context.Context,
	pool *pgxpool.Pool,
	joinRequestID string,
	decidedBy string,
) (JoinRequest, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return JoinRequest{}, classify(err)
	}
	defer tx.Rollback(ctx)

	joinReq, err := decideJoinRequest(ctx, tx, joinRequestID, decidedBy, "approved")
	if err != nil {
		return JoinRequest{}, err
	}

	_, err = tx.Exec(ctx, promoteOutsiderQuery, pgx.NamedArgs{"id": joinReq.RequesterID})
	if err != nil {
		return JoinRequest{}, classify(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return JoinRequest{}, classify(err)
	}

	return joinReq, nil
}

func RejectJoinRequest(
	ctx context.Context,
	pool *pgxpool.Pool,
	joinRequestID string,
	decidedBy string,
) (JoinRequest, error) {
	return decideJoinRequest(ctx, pool, joinRequestID, decidedBy, "rejected")
}
