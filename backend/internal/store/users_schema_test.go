package store_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Vidal322/activity-library/internal/testutil"
)

// These tests are the database's own guarantees about users.role and
// users.active, not the store's: every assertion is something Postgres refuses
// or fills in on its own. They live in store_test rather than store because
// testutil imports store, and an internal test file importing it back would be
// a cycle.

// TestMain opens the test pool and migrates it once for the whole package. With
// DB_TEST_DSN unset the tests that need a database skip themselves through
// testutil.RequirePool.
func TestMain(m *testing.M) {
	os.Exit(testutil.Run(m))
}

// queryTimeout bounds each statement, so a wedged database fails a test rather
// than hanging the suite.
const queryTimeout = 5 * time.Second

// usersTest truncates the database and returns a pool and a bounded context.
// Truncation happens at setup so a panicking test cannot poison the next one.
func usersTest(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()

	pool := testutil.RequirePool(t)
	testutil.Truncate(t, pool)

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	t.Cleanup(cancel)

	return pool, ctx
}

// assertSQLState fails unless err is a Postgres error carrying want. Matching on
// the SQLSTATE rather than the message keeps the test independent of the
// constraint's auto-generated name and of Postgres' wording.
func assertSQLState(t *testing.T, err error, want string) {
	t.Helper()

	if err == nil {
		t.Fatalf("statement succeeded, want SQLSTATE %s", want)
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("error is not a Postgres error: %v", err)
	}
	if pgErr.Code != want {
		t.Errorf("SQLSTATE = %s, want %s: %v", pgErr.Code, want, err)
	}
}

// TestUsersDefaultToOutsiderAndActive is the guarantee signup depends on: an
// account that names no standing has none. If the default ever became 'member',
// creating an account would admit you to the organization, which is the whole
// thing the outsider value exists to prevent.
func TestUsersDefaultToOutsiderAndActive(t *testing.T) {
	pool, ctx := usersTest(t)

	if _, err := pool.Exec(ctx,
		`INSERT INTO users (name, email, pass_hash)
			VALUES ('Ana Marques', 'ana@example.test', 'not-a-real-hash')`,
	); err != nil {
		t.Fatalf("could not insert an account: %v", err)
	}

	var role string
	var active bool
	if err := pool.QueryRow(ctx,
		`SELECT role, active FROM users WHERE email = 'ana@example.test'`,
	).Scan(&role, &active); err != nil {
		t.Fatalf("could not read the account back: %v", err)
	}

	if role != "outsider" {
		t.Errorf("role = %q, want %q", role, "outsider")
	}
	if !active {
		t.Error("active = false, want true")
	}
}

// TestUsersRejectUnknownRole pins the check constraint. The roles are a closed
// set in the model, and nothing in the Go build enforces that: a typo in a
// future UPDATE has to fail here or not at all.
func TestUsersRejectUnknownRole(t *testing.T) {
	pool, ctx := usersTest(t)

	_, err := pool.Exec(ctx,
		`INSERT INTO users (name, email, pass_hash, role)
			VALUES ('Bruno Costa', 'bruno@example.test', 'not-a-real-hash', 'wizard')`,
	)
	assertSQLState(t, err, pgerrcode.CheckViolation)
}

// TestUsersAcceptEveryKnownRole is the other half of the constraint: a test that
// only checks rejection passes just as well against a check that rejects
// everything. The empty-string case is here because role is text, not an enum,
// so ” is a value the column would otherwise take.
func TestUsersAcceptEveryKnownRole(t *testing.T) {
	pool, ctx := usersTest(t)

	for _, role := range []string{"outsider", "member", "admin"} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO users (name, email, pass_hash, role)
				VALUES ($1, $1 || '@example.test', 'not-a-real-hash', $1)`,
			role,
		); err != nil {
			t.Errorf("role %q was rejected, want accepted: %v", role, err)
		}
	}

	_, err := pool.Exec(ctx,
		`INSERT INTO users (name, email, pass_hash, role)
			VALUES ('Nobody', 'nobody@example.test', 'not-a-real-hash', '')`,
	)
	assertSQLState(t, err, pgerrcode.CheckViolation)
}

// TestUsersEmailUniquePerLiveAccount covers users_email_key, which is partial on
// active. Two live accounts cannot share an address, case folded; a deleted one
// releases its address so the person can register again. That release is the
// decision recorded in docs/constraints.md — deletion is final and coming back
// means a new account — and it is invisible in the Go code, so it is pinned
// here.
func TestUsersEmailUniquePerLiveAccount(t *testing.T) {
	pool, ctx := usersTest(t)

	if _, err := pool.Exec(ctx,
		`INSERT INTO users (name, email, pass_hash)
			VALUES ('Ana Marques', 'ana@example.test', 'not-a-real-hash')`,
	); err != nil {
		t.Fatalf("could not insert the first account: %v", err)
	}

	// Same address in a different case: the index is on lower(email).
	_, err := pool.Exec(ctx,
		`INSERT INTO users (name, email, pass_hash)
			VALUES ('Ana Again', 'ANA@example.test', 'not-a-real-hash')`,
	)
	assertSQLState(t, err, pgerrcode.UniqueViolation)

	if _, err := pool.Exec(ctx,
		`UPDATE users SET active = false WHERE email = 'ana@example.test'`,
	); err != nil {
		t.Fatalf("could not delete the first account: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO users (name, email, pass_hash)
			VALUES ('Ana Marques', 'ana@example.test', 'not-a-real-hash')`,
	); err != nil {
		t.Fatalf("a deleted account still holds its email address, want it released: %v", err)
	}
}
