package store_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Vidal322/activity-library/internal/store"
)

func TestCreateJoinRequestReturnsTheStoredRow(t *testing.T) {
	pool, ctx := usersTest(t)

	outsider := insertUser(t, pool, ctx, "Rui Costa", "rui@example.test", "outsider", nil)

	joinReq, err := store.CreateJoinRequest(ctx, pool, outsider, "Posso entrar?")
	if err != nil {
		t.Fatalf("could not create a join request: %v", err)
	}

	if joinReq.ID == "" {
		t.Error("ID is empty, want the generated uuid")
	}
	if joinReq.RequesterID != outsider {
		t.Errorf("RequesterID = %q, want %q", joinReq.RequesterID, outsider)
	}
	if joinReq.Message != "Posso entrar?" {
		t.Errorf("Message = %q, want %q", joinReq.Message, "Posso entrar?")
	}
	if joinReq.State != "pending" {
		t.Errorf("State = %q, want %q", joinReq.State, "pending")
	}
	if joinReq.CreatedAt.IsZero() {
		t.Error("CreatedAt is the zero time, want the value the column defaulted to")
	}
	if joinReq.DecidedBy != nil || joinReq.DecidedAt != nil {
		t.Errorf("a fresh join request carries a decision: %+v", joinReq)
	}
}

func TestCreateJoinRequestRefusesASecondPendingRequest(t *testing.T) {
	pool, ctx := usersTest(t)

	outsider := insertUser(t, pool, ctx, "Rui Costa", "rui@example.test", "outsider", nil)

	if _, err := store.CreateJoinRequest(ctx, pool, outsider, "first"); err != nil {
		t.Fatalf("could not create the first join request: %v", err)
	}

	_, err := store.CreateJoinRequest(ctx, pool, outsider, "second")
	assertConstraint(t, err, store.ErrConflict, "join_requests_pending")
}

func TestCreateJoinRequestAllowsOnePendingRequestPerAccount(t *testing.T) {
	pool, ctx := usersTest(t)

	first := insertUser(t, pool, ctx, "Rui Costa", "rui@example.test", "outsider", nil)
	second := insertUser(t, pool, ctx, "Eva Lopes", "eva@example.test", "outsider", nil)

	if _, err := store.CreateJoinRequest(ctx, pool, first, ""); err != nil {
		t.Fatalf("could not create the first account's join request: %v", err)
	}
	if _, err := store.CreateJoinRequest(ctx, pool, second, ""); err != nil {
		t.Fatalf("could not create the second account's join request: %v", err)
	}
}

func TestCreateJoinRequestAfterTheOldOneWasRejected(t *testing.T) {
	pool, ctx := usersTest(t)

	admin := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "admin", nil)
	outsider := insertUser(t, pool, ctx, "Rui Costa", "rui@example.test", "outsider", nil)

	first, err := store.CreateJoinRequest(ctx, pool, outsider, "first")
	if err != nil {
		t.Fatalf("could not create the first join request: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`UPDATE join_requests
		 SET state = 'rejected', decided_by = $2, decided_at = now()
		 WHERE id = $1`,
		first.ID, admin,
	); err != nil {
		t.Fatalf("could not reject the first join request: %v", err)
	}

	if _, err := store.CreateJoinRequest(ctx, pool, outsider, "second"); err != nil {
		t.Fatalf("could not create a join request after the first was rejected: %v", err)
	}
}

func TestCreateJoinRequestRefusesAnUnknownRequester(t *testing.T) {
	pool, ctx := usersTest(t)

	_, err := store.CreateJoinRequest(ctx, pool, "10000000-0000-7000-8000-0000000000aa", "")
	assertConstraint(t, err, store.ErrInvalid, "join_requests_requester_fkey")
}

func TestJoinRequestDecisionMustMatchState(t *testing.T) {
	pool, ctx := usersTest(t)

	admin := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "admin", nil)
	outsider := insertUser(t, pool, ctx, "Rui Costa", "rui@example.test", "outsider", nil)

	joinReq, err := store.CreateJoinRequest(ctx, pool, outsider, "")
	if err != nil {
		t.Fatalf("could not create a join request: %v", err)
	}

	tests := []struct {
		name       string
		update     string
		constraint string
	}{
		{
			"decided without a decider",
			`UPDATE join_requests SET state = 'approved', decided_at = now() WHERE id = $1`,
			"join_requests_decided_pair",
		},
		{
			"approved without a decision",
			`UPDATE join_requests SET state = 'approved' WHERE id = $1`,
			"join_requests_decided_at_iff_pending",
		},
		{
			"pending with a decision",
			`UPDATE join_requests SET decided_by = '` + admin + `', decided_at = now() WHERE id = $1`,
			"join_requests_decided_at_iff_pending",
		},
		{
			"unknown state",
			`UPDATE join_requests SET state = 'cancelled', decided_by = '` + admin + `', decided_at = now() WHERE id = $1`,
			"join_requests_state_check",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, tt.update, joinReq.ID)
			assertSQLState(t, err, "23514")

			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.ConstraintName != tt.constraint {
				t.Errorf("constraint = %s, want %s", pgErr.ConstraintName, tt.constraint)
			}
		})
	}
}
