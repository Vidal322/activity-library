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

// testSessionTTL is the session lifetime the test server runs with. It matches
// the production default, because the cookie tests assert the Max-Age it
// produces and a round number here would hide an arithmetic mistake that a real
// duration would expose.
const testSessionTTL = 720 * time.Hour

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

	return newTestServerWithConfig(t, testConfig())
}

// testConfig is what newTestServer runs with. Addr is left at its zero value
// because it belongs to the production listener and httptest picks its own
// port, but the session fields cannot be: a zero TTL writes a cookie with no
// Max-Age and a sessions row that has already expired, so every login test
// would be asserting against a session the next request would reject.
//
// CookieSecure is false to match the plain http httptest serves over. The tests
// that care about that attribute set it themselves.
func testConfig() config.Config {
	return config.Config{
		Session: config.SessionConfig{
			TTL:          testSessionTTL,
			CookieSecure: false,
		},
	}
}

// newTestServerWithConfig is newTestServer for the tests that need a particular
// configuration rather than the default one.
func newTestServerWithConfig(t *testing.T, cfg config.Config) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()

	pool := testutil.RequirePool(t)
	testutil.Truncate(t, pool)

	srv := httptest.NewServer(NewServer(cfg, pool).routes())
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
