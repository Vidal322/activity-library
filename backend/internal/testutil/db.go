package testutil

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Vidal322/activity-library/internal/store"
)

// dsnEnv is deliberately not DB_DSN, and deliberately absent from backend/.env,
// which godotenv loads at startup. Truncating is destructive, so pointing the
// harness at a database has to be a separate, explicit act.
const dsnEnv = "DB_TEST_DSN"

// testDBSuffix is what the guard below requires of the database it truncates.
const testDBSuffix = "_test"

const (
	connectTimeout  = 10 * time.Second
	migrateTimeout  = 30 * time.Second
	truncateTimeout = 10 * time.Second
)

// pool is opened once per test binary by Run. It is nil when dsnEnv is unset,
// which is the signal RequirePool turns into a skip.
var pool *pgxpool.Pool

// Run opens the pool, verifies it points at a test database, applies the
// migrations and runs the package's tests. Call it from TestMain:
//
//	func TestMain(m *testing.M) { os.Exit(testutil.Run(m)) }
//
// With dsnEnv unset it runs the tests anyway; the ones that need a database
// skip themselves through RequirePool, so `make test` stays green on a machine
// with nothing running.
func Run(m *testing.M) int {
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		return m.Run()
	}

	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()

	p, err := store.InitPool(ctx, store.PoolConfig{
		DSN: dsn,
		// A test binary runs one test at a time, but migrations and the pgx
		// stdlib adapter each want a connection of their own.
		MaxConns: 4,
		MinConns: 1,
	})
	if err != nil {
		// InitPool has already stripped the DSN out of its errors.
		log.Fatalf("testutil: connect to the test database: %v", err)
	}
	defer p.Close()

	if err := guardTestDatabase(ctx, p); err != nil {
		log.Fatalf("testutil: %v", err)
	}

	migrateCtx, cancelMigrate := context.WithTimeout(context.Background(), migrateTimeout)
	defer cancelMigrate()

	if err := store.Migrate(migrateCtx, p); err != nil {
		log.Fatalf("testutil: apply migrations to the test database: %v", err)
	}

	pool = p

	return m.Run()
}

// Pool returns the pool Run opened, or nil when there is no test database.
// Prefer RequirePool; this exists for the rare caller that wants to branch on
// availability rather than skip.
func Pool() *pgxpool.Pool { return pool }

// RequirePool returns the pool, skipping the test when dsnEnv was unset.
func RequirePool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	if pool == nil {
		t.Skipf("%s is not set: skipping the tests that need a database", dsnEnv)
	}

	return pool
}

// guardTestDatabase refuses any database whose name does not end in _test.
//
// It asks the server which database the connection actually landed in rather
// than reading the name out of the DSN: PGDATABASE, a libpq service file and a
// pooler can each redirect a connection somewhere the DSN never mentions, and
// the string that looks safest is exactly the one that would be wrong.
func guardTestDatabase(ctx context.Context, pool *pgxpool.Pool) error {
	var name string
	if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&name); err != nil {
		return fmt.Errorf("read the current database name: %w", err)
	}

	if !strings.HasSuffix(name, testDBSuffix) {
		return fmt.Errorf(
			"refusing to run against database %q: %s must point at a database whose name ends in %q, because the harness truncates every table it finds",
			name,
			dsnEnv,
			testDBSuffix,
		)
	}

	return nil
}

// Truncate empties every table in the public schema.
//
// It runs at setup rather than teardown for two reasons: a test that panics
// cannot poison the one that follows, and a failed run leaves its rows in place
// to be inspected.
func Truncate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), truncateTimeout)
	defer cancel()

	// Read the list from the catalog rather than maintaining it by hand, so a
	// table added by a later migration is covered the day it lands.
	// goose_db_version is excluded: wiping it would make the migration state
	// lie, and Run applies the migrations only once per binary.
	rows, err := pool.Query(ctx, `
		SELECT tablename
		FROM pg_tables
		WHERE schemaname = 'public'
		  AND tablename <> 'goose_db_version'
	`)
	if err != nil {
		t.Fatalf("could not list the tables to truncate: %v", err)
	}

	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("could not read the tables to truncate: %v", err)
	}

	if len(names) == 0 {
		return
	}

	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = pgx.Identifier{"public", name}.Sanitize()
	}

	// One statement for all of them: TRUNCATE takes the locks together, so no
	// foreign key is left dangling part way through.
	stmt := "TRUNCATE " + strings.Join(quoted, ", ") + " RESTART IDENTITY CASCADE"
	if _, err := pool.Exec(ctx, stmt); err != nil {
		t.Fatalf("could not truncate the test database: %v", err)
	}
}
