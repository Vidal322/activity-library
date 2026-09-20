package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Vidal322/activity-library/internal/auth"
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

// testSessionToken is the token the authenticated test requests present. It is
// a constant rather than the product of a real login because every login pays
// for an argon2 verify, and the tests below are exercising the routes behind
// requireAuth, not the password path. seedSession writes the row it points at
// exactly as handleLogin would have: the hash in the table, the token in the
// cookie.
const testSessionToken = "test-session-token"

// sessionUserEmail belongs to the account seedSession creates. It is distinct
// from the addresses the auth tests use, so a test can seed its own accounts
// without colliding with this one.
const sessionUserEmail = "session@example.com"

// testSessionUserID is that account's id, fixed rather than generated so a
// test can credit it as the author of a game and assert on what the caller may
// see of their own work. It sits outside the a1/b* run the game fixtures hand
// out, since this account outlives any one of them.
const testSessionUserID = "10000000-0000-7000-8000-0000000000f1"

// newAuthedTestServer is newTestServer with a live session already in the
// database, for the routes that now sit behind requireAuth. The cookie itself
// is not returned because nothing varies per test: authedGet builds it from
// testSessionToken.
func newAuthedTestServer(t *testing.T) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()

	srv, pool := newTestServer(t)
	seedSession(t, pool)

	return srv, pool
}

// seedSession inserts an account and a session for it. The pass_hash is a
// placeholder: nothing on these routes verifies a password, and paying for a
// real argon2 hash here would add a tenth of a second to every test that only
// wants to get past the middleware.
func seedSession(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	ctx := testContext(t)

	_, err := pool.Exec(ctx,
		`INSERT INTO users (id, name, email, pass_hash)
		 VALUES ($1, 'Session User', $2, 'not-a-real-hash')`,
		testSessionUserID, sessionUserEmail,
	)
	if err != nil {
		t.Fatalf("could not seed the session account: %v", err)
	}

	_, err = pool.Exec(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		auth.HashToken(testSessionToken), testSessionUserID, time.Now().Add(testSessionTTL),
	)
	if err != nil {
		t.Fatalf("could not seed the session: %v", err)
	}
}

// authedGet is http.Get carrying the seeded session cookie. It mirrors
// http.Get's signature so the call sites keep their own error handling, and it
// only works against a server built by newAuthedTestServer.
func authedGet(t *testing.T, url string) (*http.Response, error) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: testSessionToken})

	return http.DefaultClient.Do(req)
}

// authedPatch is authedPost for the partial update, which differs from a
// create only in the method and in carrying an id.
func authedPatch(t *testing.T, url, body string) (*http.Response, error) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPatch, url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: testSessionToken})

	return http.DefaultClient.Do(req)
}

// authedPut is authedPatch for the whole-list replace, which differs from the
// partial update only in the method.
func authedPut(t *testing.T, url, body string) (*http.Response, error) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: testSessionToken})

	return http.DefaultClient.Do(req)
}

// authedPost is authedGet for the write routes. The body is passed as a string
// rather than a struct because several tests send JSON that no Go type would
// produce: a field the handler refuses to recognise is the point of the test.
func authedPost(t *testing.T, url, body string) (*http.Response, error) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: testSessionToken})

	return http.DefaultClient.Do(req)
}
