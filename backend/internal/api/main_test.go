package api

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Vidal322/activity-library/internal/config"
	"github.com/Vidal322/activity-library/internal/testutil"
)

// TestMain opens the test pool and migrates it once for the whole package. With
// DB_TEST_DSN unset it still runs the tests; the ones that need a database skip
// themselves through newTestServer.
func TestMain(m *testing.M) {
	os.Exit(testutil.Run(m))
}

// queryTimeout bounds the setup statements the tests run directly, so a wedged
// database fails a test instead of hanging the suite.
const queryTimeout = 5 * time.Second

// newTestServer truncates the database and serves the real router over a real
// socket, so a test exercises routing, middleware and encoding rather than
// calling a handler function.
//
// It returns the pool alongside the server because every handler test has rows
// to seed, and it must be the pool this server reads through. Truncation
// happens here, at setup: seed after this call, never before.
func newTestServer(t *testing.T) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()

	pool := testutil.RequirePool(t)
	testutil.Truncate(t, pool)

	// The zero Config is enough: Addr belongs to the production listener, and
	// httptest picks its own port.
	srv := httptest.NewServer(NewServer(config.Config{}, pool).routes())
	t.Cleanup(srv.Close)

	return srv, pool
}

// testContext bounds a setup query and ties it to the test's lifetime.
func testContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	t.Cleanup(cancel)

	return ctx
}
