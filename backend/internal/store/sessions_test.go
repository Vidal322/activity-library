package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Vidal322/activity-library/internal/store"
)

// These tests cover the sessions table and the three store functions over it.
// They share TestMain, queryTimeout and usersTest with users_schema_test.go, and
// insertUser with users_test.go: a session needs an account to belong to.

// sessionLifetime is how far ahead a live fixture expires. Long enough that a
// slow test cannot expire its own session mid-run.
const sessionLifetime = time.Hour

// newSession writes one live session for userID and returns its token hash. The
// hash is a plain label rather than a real digest: nothing in the store looks at
// its shape, and a readable value makes a failure easier to read.
func newSession(t *testing.T, pool *pgxpool.Pool, ctx context.Context, userID, tokenHash string) store.Session {
	t.Helper()

	session, err := store.CreateSession(ctx, pool, userID, tokenHash, time.Now().Add(sessionLifetime))
	if err != nil {
		t.Fatalf("could not create a session: %v", err)
	}

	return session
}

// TestCreateSessionReturnsTheStoredRow is the test the struct tags exist for.
// RowToStructByName matches on names, so a missing or misspelled db tag is
// invisible at compile time and shows up only as a scan that fails or a field
// left at its zero value.
func TestCreateSessionReturnsTheStoredRow(t *testing.T) {
	pool, ctx := usersTest(t)

	userID := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "member", nil)

	// timestamptz keeps microseconds and time.Time keeps nanoseconds, so a
	// value that has been through the column is never Equal to the one that
	// went in. Truncating here rather than comparing loosely keeps the
	// assertion exact about what the column actually promises.
	expires := time.Now().Add(sessionLifetime).Truncate(time.Microsecond)

	session, err := store.CreateSession(ctx, pool, userID, "hash-ana", expires)
	if err != nil {
		t.Fatalf("could not create a session: %v", err)
	}

	if session.TokenHash != "hash-ana" {
		t.Errorf("TokenHash = %q, want %q", session.TokenHash, "hash-ana")
	}
	if session.UserID != userID {
		t.Errorf("UserID = %q, want %q", session.UserID, userID)
	}
	if !session.ExpiresAt.Equal(expires) {
		t.Errorf("ExpiresAt = %v, want %v", session.ExpiresAt, expires)
	}
	if session.CreatedAt.IsZero() {
		t.Error("CreatedAt is the zero time, want the value the column defaulted to")
	}
	if session.RevokedAt != nil {
		t.Errorf("RevokedAt = %v, want nil on a fresh session", *session.RevokedAt)
	}

	// The row the caller was handed has to be the row that landed, not a struct
	// assembled from the arguments.
	stored, err := store.GetSession(ctx, pool, "hash-ana")
	if err != nil {
		t.Fatalf("could not read the new session back: %v", err)
	}
	if stored.UserID != session.UserID {
		t.Errorf("stored UserID = %q, want %q", stored.UserID, session.UserID)
	}
}

// TestGetSessionFindsTheRightSession guards against a WHERE clause that reads
// the wrong row: with one session in the table, a query matching everything
// passes just as well as one matching on the token hash. Handing a request the
// wrong session would sign it in as the wrong person.
func TestGetSessionFindsTheRightSession(t *testing.T) {
	pool, ctx := usersTest(t)

	ana := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "member", nil)
	bruno := insertUser(t, pool, ctx, "Bruno Costa", "bruno@example.test", "admin", nil)

	newSession(t, pool, ctx, ana, "hash-ana")
	newSession(t, pool, ctx, bruno, "hash-bruno")

	session, err := store.GetSession(ctx, pool, "hash-bruno")
	if err != nil {
		t.Fatalf("could not read the session back: %v", err)
	}

	if session.UserID != bruno {
		t.Errorf("UserID = %q, want Bruno's account %q", session.UserID, bruno)
	}
}

// TestGetSessionRejectsAnExpiredSession is the assertion issue #28 names, and
// the reason expires_at is filtered in SQL rather than compared in Go. Without
// the clause a cookie would work forever: expiry is the only thing that ends a
// session nobody explicitly revokes.
func TestGetSessionRejectsAnExpiredSession(t *testing.T) {
	pool, ctx := usersTest(t)

	userID := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "member", nil)

	if _, err := store.CreateSession(ctx, pool, userID, "hash-ana", time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("could not create an already-expired session: %v", err)
	}

	_, err := store.GetSession(ctx, pool, "hash-ana")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("error = %v, want store.ErrNotFound", err)
	}
}

// TestGetSessionRejectsARevokedSession pins the other half of invariant 34
// ahead of the work that writes revoked_at. Revoking is what makes removal,
// demotion and deletion take effect immediately rather than at next login, so a
// lookup that ignored the column would make all of that silently useless.
func TestGetSessionRejectsARevokedSession(t *testing.T) {
	pool, ctx := usersTest(t)

	userID := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "member", nil)
	newSession(t, pool, ctx, userID, "hash-ana")

	if _, err := pool.Exec(ctx,
		`UPDATE sessions SET revoked_at = now() WHERE token_hash = $1`, "hash-ana",
	); err != nil {
		t.Fatalf("could not revoke the session: %v", err)
	}

	_, err := store.GetSession(ctx, pool, "hash-ana")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("error = %v, want store.ErrNotFound", err)
	}
}

// TestGetSessionNotFound pins the mapping the auth middleware's 401 depends on.
// pgx reports no rows as its own sentinel; classify turns that into ErrNotFound.
// It matters that this is the same error an expired or revoked session gives:
// the caller must not be able to tell a token that was never real from one that
// has stopped working.
func TestGetSessionNotFound(t *testing.T) {
	pool, ctx := usersTest(t)

	userID := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "member", nil)
	newSession(t, pool, ctx, userID, "hash-ana")

	_, err := store.GetSession(ctx, pool, "a-hash-nobody-holds")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("error = %v, want store.ErrNotFound", err)
	}
}

// TestDeleteSessionRemovesIt is logout. Clearing the cookie alone would leave a
// working session behind for anyone who kept a copy of the value, so the row
// has to go server-side.
func TestDeleteSessionRemovesIt(t *testing.T) {
	pool, ctx := usersTest(t)

	userID := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "member", nil)
	newSession(t, pool, ctx, userID, "hash-ana")

	if err := store.DeleteSession(ctx, pool, "hash-ana"); err != nil {
		t.Fatalf("could not delete the session: %v", err)
	}

	if _, err := store.GetSession(ctx, pool, "hash-ana"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("error = %v, want store.ErrNotFound after a delete", err)
	}
}

// TestDeleteSessionLeavesOtherSessionsAlone is the same worry as the lookup
// test, with worse consequences: a DELETE missing its WHERE clause would log
// out every person signed in whenever anyone signed out.
func TestDeleteSessionLeavesOtherSessionsAlone(t *testing.T) {
	pool, ctx := usersTest(t)

	ana := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "member", nil)
	bruno := insertUser(t, pool, ctx, "Bruno Costa", "bruno@example.test", "admin", nil)

	newSession(t, pool, ctx, ana, "hash-ana")
	newSession(t, pool, ctx, bruno, "hash-bruno")

	if err := store.DeleteSession(ctx, pool, "hash-ana"); err != nil {
		t.Fatalf("could not delete Ana's session: %v", err)
	}

	if _, err := store.GetSession(ctx, pool, "hash-bruno"); err != nil {
		t.Fatalf("Bruno's session did not survive Ana's logout: %v", err)
	}
}

// TestDeleteSessionNotFound records a decision rather than a guarantee. A delete
// matching nothing could reasonably be a no-op, but reporting it lets the logout
// handler tell a real sign-out from a stale or forged cookie. If that
// distinction stops being wanted, this is the test that changes.
func TestDeleteSessionNotFound(t *testing.T) {
	pool, ctx := usersTest(t)

	err := store.DeleteSession(ctx, pool, "a-hash-nobody-holds")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("error = %v, want store.ErrNotFound", err)
	}
}

// TestCreateSessionRequiresAnExistingAccount pins the foreign key reaching the
// caller as ErrInvalid rather than as a raw pgx error a handler would turn into
// a 500. The constraint name rides along inside ConstraintError, which is how
// writeStoreError names the offending field.
func TestCreateSessionRequiresAnExistingAccount(t *testing.T) {
	pool, ctx := usersTest(t)

	// A syntactically valid uuid that no account carries.
	const missing = "00000000-0000-7000-8000-000000000000"

	_, err := store.CreateSession(ctx, pool, missing, "hash-nobody", time.Now().Add(sessionLifetime))
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("error = %v, want store.ErrInvalid", err)
	}

	var ce *store.ConstraintError
	if !errors.As(err, &ce) {
		t.Fatalf("error is not a ConstraintError: %v", err)
	}
	if ce.Constraint != "sessions_user_id_fkey" {
		t.Errorf("Constraint = %q, want %q", ce.Constraint, "sessions_user_id_fkey")
	}
}

// TestCreateSessionRejectsADuplicateTokenHash is the primary key seen from the
// store. Two accounts holding one token hash would mean one cookie opening two
// sessions, and the lookup could not say which.
func TestCreateSessionRejectsADuplicateTokenHash(t *testing.T) {
	pool, ctx := usersTest(t)

	ana := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "member", nil)
	bruno := insertUser(t, pool, ctx, "Bruno Costa", "bruno@example.test", "admin", nil)

	newSession(t, pool, ctx, ana, "hash-collision")

	_, err := store.CreateSession(ctx, pool, bruno, "hash-collision", time.Now().Add(sessionLifetime))
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("error = %v, want store.ErrConflict", err)
	}

	var ce *store.ConstraintError
	if !errors.As(err, &ce) {
		t.Fatalf("error is not a ConstraintError: %v", err)
	}
	if ce.Constraint != "sessions_pkey" {
		t.Errorf("Constraint = %q, want %q", ce.Constraint, "sessions_pkey")
	}
}

// TestDeletingAnAccountDestroysItsSessions is constraint 32, and the reason the
// foreign key carries ON DELETE CASCADE. Normal deletion is soft, so this is the
// backstop for a row that really does go: without the cascade the delete would
// be refused outright and an orphaned session could otherwise outlive the
// account it names.
func TestDeletingAnAccountDestroysItsSessions(t *testing.T) {
	pool, ctx := usersTest(t)

	ana := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "member", nil)
	bruno := insertUser(t, pool, ctx, "Bruno Costa", "bruno@example.test", "admin", nil)

	newSession(t, pool, ctx, ana, "hash-ana")
	newSession(t, pool, ctx, bruno, "hash-bruno")

	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, ana); err != nil {
		t.Fatalf("could not delete the account: %v", err)
	}

	if _, err := store.GetSession(ctx, pool, "hash-ana"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("error = %v, want store.ErrNotFound: the session outlived its account", err)
	}

	if _, err := store.GetSession(ctx, pool, "hash-bruno"); err != nil {
		t.Fatalf("an unrelated session was destroyed too: %v", err)
	}
}

// TestSessionsRejectAnEmptyTokenHash is the CHECK constraint reaching the
// caller. The store never builds the hash itself, so an empty string means a
// caller that forgot to hash: better refused at the column than stored as a row
// that any other empty-hash lookup would match.
func TestSessionsRejectAnEmptyTokenHash(t *testing.T) {
	pool, ctx := usersTest(t)

	userID := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "member", nil)

	_, err := store.CreateSession(ctx, pool, userID, "", time.Now().Add(sessionLifetime))
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("error = %v, want store.ErrInvalid", err)
	}
}
