package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Vidal322/activity-library/internal/store"
)

// These tests cover the invitations table and the store functions over it. They
// share TestMain, queryTimeout and usersTest with users_schema_test.go, and
// insertUser with users_test.go: every invitation is sent by an account.

// invitationLifetime is how far ahead a live fixture expires. Long enough that a
// slow test cannot expire its own invitation mid-run.
const invitationLifetime = time.Hour

// newInvitation writes one open invitation from invitedBy. As with sessions, the
// hash is a readable label rather than a real digest: the store never looks at
// its shape.
func newInvitation(t *testing.T, pool *pgxpool.Pool, ctx context.Context, email, invitedBy, tokenHash string) store.Invitation {
	t.Helper()

	invitation, err := store.CreateInvitation(ctx, pool, email, invitedBy, tokenHash, time.Now().Add(invitationLifetime))
	if err != nil {
		t.Fatalf("could not create an invitation: %v", err)
	}

	return invitation
}

// assertConstraint fails unless err is sentinel raised by the named constraint.
// Several constraints map to the same sentinel, and the name is what tells the
// API which message to send.
func assertConstraint(t *testing.T, err error, sentinel error, constraint string) {
	t.Helper()

	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want %v", err, sentinel)
	}

	var ce *store.ConstraintError
	if !errors.As(err, &ce) || ce.Constraint != constraint {
		t.Fatalf("error = %v, want it raised by %s", err, constraint)
	}
}

// TestCreateInvitationReturnsTheStoredRow is the struct-tag test: a missing or
// misspelled db tag compiles fine and only shows up as a failed scan.
func TestCreateInvitationReturnsTheStoredRow(t *testing.T) {
	pool, ctx := usersTest(t)

	admin := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "admin", nil)

	// timestamptz keeps microseconds; see TestCreateSessionReturnsTheStoredRow.
	expires := time.Now().Add(invitationLifetime).Truncate(time.Microsecond)

	invitation, err := store.CreateInvitation(ctx, pool, "new@example.test", admin, "hash-new", expires)
	if err != nil {
		t.Fatalf("could not create an invitation: %v", err)
	}

	if invitation.ID == "" {
		t.Error("ID is empty, want the generated uuid")
	}
	if invitation.Email != "new@example.test" {
		t.Errorf("Email = %q, want %q", invitation.Email, "new@example.test")
	}
	if invitation.TokenHash != "hash-new" {
		t.Errorf("TokenHash = %q, want %q", invitation.TokenHash, "hash-new")
	}
	if invitation.InvitedBy != admin {
		t.Errorf("InvitedBy = %q, want %q", invitation.InvitedBy, admin)
	}
	if !invitation.ExpiresAt.Equal(expires) {
		t.Errorf("ExpiresAt = %v, want %v", invitation.ExpiresAt, expires)
	}
	if invitation.CreatedAt.IsZero() {
		t.Error("CreatedAt is the zero time, want the value the column defaulted to")
	}
	if invitation.AcceptedBy != nil || invitation.AcceptedAt != nil ||
		invitation.RevokedBy != nil || invitation.RevokedAt != nil {
		t.Errorf("a fresh invitation carries acceptance or revocation: %+v", invitation)
	}
}

// TestCreateInvitationRefusesASecondOpenInvitation pins
// invitations_pending_email_key, including its lower(): an admin re-inviting
// the same person with different capitalisation must not leave two live links.
func TestCreateInvitationRefusesASecondOpenInvitation(t *testing.T) {
	pool, ctx := usersTest(t)

	admin := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "admin", nil)
	newInvitation(t, pool, ctx, "new@example.test", admin, "hash-first")

	_, err := store.CreateInvitation(ctx, pool, "NEW@example.test", admin, "hash-second", time.Now().Add(invitationLifetime))
	assertConstraint(t, err, store.ErrConflict, "invitations_pending_email_key")
}

// TestCreateInvitationAfterRevokingTheOpenOne is the way out of the rule above:
// the index only covers open invitations, so revoking frees the address.
func TestCreateInvitationAfterRevokingTheOpenOne(t *testing.T) {
	pool, ctx := usersTest(t)

	admin := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "admin", nil)
	first := newInvitation(t, pool, ctx, "new@example.test", admin, "hash-first")

	if _, err := store.RevokeInvitation(ctx, pool, first.ID, admin); err != nil {
		t.Fatalf("could not revoke the first invitation: %v", err)
	}

	if _, err := store.CreateInvitation(ctx, pool, "new@example.test", admin, "hash-second", time.Now().Add(invitationLifetime)); err != nil {
		t.Fatalf("could not invite the address again after revoking: %v", err)
	}
}

// TestGetInvitationByTokenHashFindsTheRightInvitation guards against a WHERE
// clause that reads the wrong row: with a single invitation in the table, a
// query matching everything would pass too.
func TestGetInvitationByTokenHashFindsTheRightInvitation(t *testing.T) {
	pool, ctx := usersTest(t)

	admin := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "admin", nil)
	newInvitation(t, pool, ctx, "first@example.test", admin, "hash-first")
	newInvitation(t, pool, ctx, "second@example.test", admin, "hash-second")

	invitation, err := store.GetInvitationByTokenHash(ctx, pool, "hash-second")
	if err != nil {
		t.Fatalf("could not read the invitation: %v", err)
	}

	if invitation.Email != "second@example.test" {
		t.Errorf("Email = %q, want %q", invitation.Email, "second@example.test")
	}
}

// TestGetInvitationByTokenHashOnlyFindsOpenInvitations pins the filter: a link
// that has been used, revoked or has expired must stop working, and must fail
// exactly like a token that never existed.
func TestGetInvitationByTokenHashOnlyFindsOpenInvitations(t *testing.T) {
	tests := []struct {
		name  string
		close string
	}{
		{"expired", `UPDATE invitations SET expires_at = now() - interval '1 minute'`},
		{"revoked", `UPDATE invitations SET revoked_at = now(), revoked_by = invited_by`},
		{"accepted", `UPDATE invitations SET accepted_at = now(), accepted_by = invited_by`},
		{"unknown", ``},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool, ctx := usersTest(t)

			admin := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "admin", nil)
			newInvitation(t, pool, ctx, "new@example.test", admin, "hash-new")

			hash := "hash-new"
			if tt.close == "" {
				hash = "a-hash-nobody-holds"
			} else if _, err := pool.Exec(ctx, tt.close); err != nil {
				t.Fatalf("could not close the invitation: %v", err)
			}

			_, err := store.GetInvitationByTokenHash(ctx, pool, hash)
			if !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("error = %v, want store.ErrNotFound", err)
			}
		})
	}
}

// TestAcceptInvitationCreatesTheAccount pins what an accepted invitation turns
// into: a member account at the invited address, and an invitation that names
// that account and can no longer be used.
func TestAcceptInvitationCreatesTheAccount(t *testing.T) {
	pool, ctx := usersTest(t)

	admin := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "admin", nil)
	newInvitation(t, pool, ctx, "new@example.test", admin, "hash-new")

	user, err := store.AcceptInvitation(ctx, pool, "hash-new", "New Person", "not-a-real-hash", nil)
	if err != nil {
		t.Fatalf("could not accept the invitation: %v", err)
	}

	if user.Email != "new@example.test" {
		t.Errorf("Email = %q, want the invited address", user.Email)
	}
	if user.Name != "New Person" {
		t.Errorf("Name = %q, want %q", user.Name, "New Person")
	}
	if user.Role != "member" {
		t.Errorf("Role = %q, want member", user.Role)
	}

	var acceptedBy *string
	var acceptedAt *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT accepted_by, accepted_at FROM invitations WHERE token_hash = 'hash-new'`,
	).Scan(&acceptedBy, &acceptedAt); err != nil {
		t.Fatalf("could not read the invitation back: %v", err)
	}
	if acceptedBy == nil || *acceptedBy != user.ID {
		t.Errorf("accepted_by = %v, want the new account %q", acceptedBy, user.ID)
	}
	if acceptedAt == nil {
		t.Error("accepted_at is NULL, want the time of acceptance")
	}
}

// TestAcceptInvitationOnlyOnce is the reason the invitation is locked: a link
// that worked once must not create a second account.
func TestAcceptInvitationOnlyOnce(t *testing.T) {
	pool, ctx := usersTest(t)

	admin := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "admin", nil)
	newInvitation(t, pool, ctx, "new@example.test", admin, "hash-new")

	if _, err := store.AcceptInvitation(ctx, pool, "hash-new", "New Person", "not-a-real-hash", nil); err != nil {
		t.Fatalf("could not accept the invitation: %v", err)
	}

	_, err := store.AcceptInvitation(ctx, pool, "hash-new", "Someone Else", "not-a-real-hash", nil)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("error = %v, want store.ErrNotFound on a used invitation", err)
	}
}

// TestAcceptInvitationRacesCreateOneAccount sends the same link several times
// at once. Without FOR UPDATE every request could read the invitation as open
// before any of them marked it used.
func TestAcceptInvitationRacesCreateOneAccount(t *testing.T) {
	pool, ctx := usersTest(t)

	admin := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "admin", nil)
	newInvitation(t, pool, ctx, "new@example.test", admin, "hash-new")

	const racers = 5
	errs := make([]error, racers)

	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = store.AcceptInvitation(ctx, pool, "hash-new", "New Person", "not-a-real-hash", nil)
		}()
	}
	wg.Wait()

	won := 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, store.ErrNotFound):
		default:
			t.Errorf("a losing accept failed with %v, want store.ErrNotFound", err)
		}
	}
	if won != 1 {
		t.Errorf("%d accepts succeeded, want exactly 1", won)
	}
}

// TestAcceptInvitationForATakenAddressKeepsItOpen covers an invitation sent to
// someone who registered in the meantime. The account insert fails, and the
// transaction must take the invitation's update down with it.
func TestAcceptInvitationForATakenAddressKeepsItOpen(t *testing.T) {
	pool, ctx := usersTest(t)

	admin := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "admin", nil)
	insertUser(t, pool, ctx, "Already Here", "taken@example.test", "member", nil)
	newInvitation(t, pool, ctx, "taken@example.test", admin, "hash-taken")

	_, err := store.AcceptInvitation(ctx, pool, "hash-taken", "New Person", "not-a-real-hash", nil)
	assertConstraint(t, err, store.ErrConflict, "users_email_key")

	if _, err := store.GetInvitationByTokenHash(ctx, pool, "hash-taken"); err != nil {
		t.Fatalf("the invitation did not stay open after the failed accept: %v", err)
	}
}

// TestRevokeInvitationRecordsWhoAndWhen pins the returned row: the admin screen
// shows the result of a revoke without reading the invitation again.
func TestRevokeInvitationRecordsWhoAndWhen(t *testing.T) {
	pool, ctx := usersTest(t)

	admin := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "admin", nil)
	other := insertUser(t, pool, ctx, "Bruno Costa", "bruno@example.test", "admin", nil)
	invitation := newInvitation(t, pool, ctx, "new@example.test", admin, "hash-new")

	revoked, err := store.RevokeInvitation(ctx, pool, invitation.ID, other)
	if err != nil {
		t.Fatalf("could not revoke the invitation: %v", err)
	}

	if revoked.ID != invitation.ID {
		t.Errorf("ID = %q, want %q", revoked.ID, invitation.ID)
	}
	if revoked.RevokedBy == nil || *revoked.RevokedBy != other {
		t.Errorf("RevokedBy = %v, want the revoking admin %q", revoked.RevokedBy, other)
	}
	if revoked.RevokedAt == nil {
		t.Error("RevokedAt is nil, want the time of revocation")
	}

	if _, err := store.GetInvitationByTokenHash(ctx, pool, "hash-new"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("error = %v, want the revoked link to stop working", err)
	}
}

// TestRevokeInvitationAcceptsAnExpiredOne is deliberate: an expired invitation
// still holds invitations_pending_email_key, and revoking it is the only way to
// invite the address again.
func TestRevokeInvitationAcceptsAnExpiredOne(t *testing.T) {
	pool, ctx := usersTest(t)

	admin := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "admin", nil)
	invitation, err := store.CreateInvitation(ctx, pool, "new@example.test", admin, "hash-new", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("could not create an expired invitation: %v", err)
	}

	if _, err := store.RevokeInvitation(ctx, pool, invitation.ID, admin); err != nil {
		t.Fatalf("could not revoke an expired invitation: %v", err)
	}
}

// TestRevokeInvitationRefusesAClosedOne covers the rows the WHERE leaves out.
// Revoking twice would overwrite who revoked it first, and revoking an accepted
// invitation would break invitations_accepted_or_revoked.
func TestRevokeInvitationRefusesAClosedOne(t *testing.T) {
	pool, ctx := usersTest(t)

	admin := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "admin", nil)
	revoked := newInvitation(t, pool, ctx, "revoked@example.test", admin, "hash-revoked")
	accepted := newInvitation(t, pool, ctx, "accepted@example.test", admin, "hash-accepted")

	if _, err := store.RevokeInvitation(ctx, pool, revoked.ID, admin); err != nil {
		t.Fatalf("could not revoke the invitation: %v", err)
	}
	if _, err := store.AcceptInvitation(ctx, pool, "hash-accepted", "New Person", "not-a-real-hash", nil); err != nil {
		t.Fatalf("could not accept the invitation: %v", err)
	}

	for name, id := range map[string]string{
		"revoked":  revoked.ID,
		"accepted": accepted.ID,
		"unknown":  "10000000-0000-7000-8000-00000000dead",
	} {
		if _, err := store.RevokeInvitation(ctx, pool, id, admin); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: error = %v, want store.ErrNotFound", name, err)
		}
	}
}
