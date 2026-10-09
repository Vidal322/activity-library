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

func TestListPendingJoinRequestsOnlyListsPendingOldestFirst(t *testing.T) {
	pool, ctx := usersTest(t)

	admin := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "admin", nil)
	older := insertUser(t, pool, ctx, "Rui Costa", "rui@example.test", "outsider", nil)
	newer := insertUser(t, pool, ctx, "Eva Lopes", "eva@example.test", "outsider", nil)
	rejected := insertUser(t, pool, ctx, "Luís Sá", "luis@example.test", "outsider", nil)

	newerReq, err := store.CreateJoinRequest(ctx, pool, newer, "newer")
	if err != nil {
		t.Fatalf("could not create the newer join request: %v", err)
	}
	olderReq, err := store.CreateJoinRequest(ctx, pool, older, "older")
	if err != nil {
		t.Fatalf("could not create the older join request: %v", err)
	}
	rejectedReq, err := store.CreateJoinRequest(ctx, pool, rejected, "rejected")
	if err != nil {
		t.Fatalf("could not create the rejected join request: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`UPDATE join_requests SET created_at = now() - interval '1 day' WHERE id = $1`,
		olderReq.ID,
	); err != nil {
		t.Fatalf("could not backdate the older join request: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE join_requests
		 SET state = 'rejected', decided_by = $2, decided_at = now()
		 WHERE id = $1`,
		rejectedReq.ID, admin,
	); err != nil {
		t.Fatalf("could not reject the join request: %v", err)
	}

	got, err := store.ListPendingJoinRequests(ctx, pool)
	if err != nil {
		t.Fatalf("could not list the pending join requests: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d join requests, want 2: %+v", len(got), got)
	}
	if got[0].ID != olderReq.ID || got[1].ID != newerReq.ID {
		t.Errorf("order = [%s %s], want [%s %s]", got[0].ID, got[1].ID, olderReq.ID, newerReq.ID)
	}
	if got[0].RequesterName != "Rui Costa" || got[0].RequesterEmail != "rui@example.test" {
		t.Errorf("requester = %q <%s>, want %q <%s>", got[0].RequesterName, got[0].RequesterEmail, "Rui Costa", "rui@example.test")
	}
	if got[0].RequesterID != older || got[0].Message != "older" || got[0].State != "pending" {
		t.Errorf("join request fields not read back: %+v", got[0])
	}
}

func TestListPendingJoinRequestsIsEmptyWithNoRequests(t *testing.T) {
	pool, ctx := usersTest(t)

	got, err := store.ListPendingJoinRequests(ctx, pool)
	if err != nil {
		t.Fatalf("could not list the pending join requests: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d join requests, want none", len(got))
	}
}

func TestAcceptJoinRequestApprovesAndPromotesTheRequester(t *testing.T) {
	pool, ctx := usersTest(t)

	admin := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "admin", nil)
	outsider := insertUser(t, pool, ctx, "Rui Costa", "rui@example.test", "outsider", nil)

	created, err := store.CreateJoinRequest(ctx, pool, outsider, "Posso entrar?")
	if err != nil {
		t.Fatalf("could not create a join request: %v", err)
	}

	got, err := store.AcceptJoinRequest(ctx, pool, created.ID, admin)
	if err != nil {
		t.Fatalf("could not accept the join request: %v", err)
	}

	if got.ID != created.ID {
		t.Errorf("ID = %q, want %q", got.ID, created.ID)
	}
	if got.State != "approved" {
		t.Errorf("State = %q, want %q", got.State, "approved")
	}
	if got.DecidedBy == nil || *got.DecidedBy != admin {
		t.Errorf("DecidedBy = %v, want %q", got.DecidedBy, admin)
	}
	if got.DecidedAt == nil {
		t.Error("DecidedAt is nil, want the decision time")
	}

	user, err := store.GetUserByID(ctx, pool, outsider)
	if err != nil {
		t.Fatalf("could not read the requester back: %v", err)
	}
	if user.Role != "member" {
		t.Errorf("requester role = %q, want %q", user.Role, "member")
	}
}

func TestAcceptJoinRequestOnlyDecidesOnce(t *testing.T) {
	pool, ctx := usersTest(t)

	admin := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "admin", nil)
	outsider := insertUser(t, pool, ctx, "Rui Costa", "rui@example.test", "outsider", nil)

	created, err := store.CreateJoinRequest(ctx, pool, outsider, "")
	if err != nil {
		t.Fatalf("could not create a join request: %v", err)
	}

	if _, err := store.AcceptJoinRequest(ctx, pool, created.ID, admin); err != nil {
		t.Fatalf("could not accept the join request: %v", err)
	}

	_, err = store.AcceptJoinRequest(ctx, pool, created.ID, admin)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("error = %v, want %v", err, store.ErrNotFound)
	}
}

func TestAcceptJoinRequestRefusesARejectedRequest(t *testing.T) {
	pool, ctx := usersTest(t)

	admin := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "admin", nil)
	outsider := insertUser(t, pool, ctx, "Rui Costa", "rui@example.test", "outsider", nil)

	created, err := store.CreateJoinRequest(ctx, pool, outsider, "")
	if err != nil {
		t.Fatalf("could not create a join request: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`UPDATE join_requests
		 SET state = 'rejected', decided_by = $2, decided_at = now()
		 WHERE id = $1`,
		created.ID, admin,
	); err != nil {
		t.Fatalf("could not reject the join request: %v", err)
	}

	_, err = store.AcceptJoinRequest(ctx, pool, created.ID, admin)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("error = %v, want %v", err, store.ErrNotFound)
	}

	user, err := store.GetUserByID(ctx, pool, outsider)
	if err != nil {
		t.Fatalf("could not read the requester back: %v", err)
	}
	if user.Role != "outsider" {
		t.Errorf("requester role = %q, want %q", user.Role, "outsider")
	}
}

func TestAcceptJoinRequestRefusesAnUnknownRequest(t *testing.T) {
	pool, ctx := usersTest(t)

	admin := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "admin", nil)

	_, err := store.AcceptJoinRequest(ctx, pool, "10000000-0000-7000-8000-0000000000aa", admin)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("error = %v, want %v", err, store.ErrNotFound)
	}
}

func TestAcceptJoinRequestNeverDemotesTheRequester(t *testing.T) {
	pool, ctx := usersTest(t)

	admin := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "admin", nil)
	other := insertUser(t, pool, ctx, "Eva Lopes", "eva@example.test", "admin", nil)

	created, err := store.CreateJoinRequest(ctx, pool, other, "")
	if err != nil {
		t.Fatalf("could not create a join request: %v", err)
	}

	if _, err := store.AcceptJoinRequest(ctx, pool, created.ID, admin); err != nil {
		t.Fatalf("could not accept the join request: %v", err)
	}

	user, err := store.GetUserByID(ctx, pool, other)
	if err != nil {
		t.Fatalf("could not read the requester back: %v", err)
	}
	if user.Role != "admin" {
		t.Errorf("requester role = %q, want %q", user.Role, "admin")
	}
}
