package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Vidal322/activity-library/internal/auth"
	"github.com/Vidal322/activity-library/internal/config"
)

// These tests drive POST /v1/auth/login over a real socket, like the user tests
// next door, and assert on the wire: the status, the body bytes, the Set-Cookie
// header and the sessions row that should or should not exist afterwards.
//
// Every seeded account pays for a real argon2id hash, and every login pays for
// a real verify, so each case costs roughly a tenth of a second and 64 MiB.
// That is the point — the cost is half of what the unknown-email path exists to
// imitate — but it is why these cases seed one account rather than several.

const testEmail = "pedro@example.com"

// postLogin sends a login and returns the response with its body already read,
// so the caller can reach both the cookies and the bytes. The body is returned
// raw rather than decoded, because the tests that matter compare two responses
// to each other and a struct would quietly normalise away a difference.
func postLogin(t *testing.T, baseURL, body string) (*http.Response, []byte) {
	t.Helper()

	res, err := http.Post(baseURL+"/v1/auth/login", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/auth/login: %v", err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("could not read the response body: %v", err)
	}

	return res, raw
}

func loginBody(email, password string) string {
	return fmt.Sprintf(`{"email": %q, "password": %q}`, email, password)
}

// seedAccount inserts one account with a genuine argon2id hash, so that the
// handler's Verify runs against the same thing production would hand it.
func seedAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, email, password string, active bool) string {
	t.Helper()

	hash, err := auth.Hash(password)
	if err != nil {
		t.Fatalf("could not hash the seed password: %v", err)
	}

	var id string
	err = pool.QueryRow(ctx,
		`INSERT INTO users (name, email, pass_hash, active) VALUES ('Pedro', $1, $2, $3) RETURNING id`,
		email, hash, active,
	).Scan(&id)
	if err != nil {
		t.Fatalf("could not seed an account: %v", err)
	}

	return id
}

// sessionCookie returns the session cookie from a response, or fails.
func sessionCookieFrom(t *testing.T, res *http.Response) *http.Cookie {
	t.Helper()

	for _, c := range res.Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}

	t.Fatalf("no %s cookie in the response, got %v", sessionCookieName, res.Cookies())
	return nil
}

func hasSessionCookie(res *http.Response) bool {
	for _, c := range res.Cookies() {
		if c.Name == sessionCookieName {
			return true
		}
	}
	return false
}

func countSessions(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatalf("could not count the sessions: %v", err)
	}
	return n
}

// onlySession reads the single row the tests expect, failing if there is not
// exactly one.
func onlySession(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (tokenHash, userID string, expiresAt time.Time) {
	t.Helper()

	if n := countSessions(t, ctx, pool); n != 1 {
		t.Fatalf("sessions holds %d rows, want exactly 1", n)
	}

	err := pool.QueryRow(ctx,
		`SELECT token_hash, user_id, expires_at FROM sessions`,
	).Scan(&tokenHash, &userID, &expiresAt)
	if err != nil {
		t.Fatalf("could not read the session: %v", err)
	}

	return tokenHash, userID, expiresAt
}

func TestHandleLoginReturnsTheAccount(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	id := seedAccount(t, ctx, pool, testEmail, testPassword, true)

	res, body := postLogin(t, srv.URL, loginBody(testEmail, testPassword))

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d — body %s", res.StatusCode, http.StatusOK, body)
	}

	got := decodeUser(t, body)
	if got.ID != id {
		t.Errorf("id = %q, want %q", got.ID, id)
	}
	if !got.Active {
		t.Error("active = false, want true")
	}
}

// TestHandleLoginStoresTheHashAndSendsTheToken is the test that would have
// caught the design inverted. A handler that puts the hash in the cookie still
// returns a 200 with a cookie and still writes a row, so nothing above this
// notices; only comparing the two values does.
func TestHandleLoginStoresTheHashAndSendsTheToken(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	id := seedAccount(t, ctx, pool, testEmail, testPassword, true)

	res, _ := postLogin(t, srv.URL, loginBody(testEmail, testPassword))
	cookie := sessionCookieFrom(t, res)

	tokenHash, userID, expiresAt := onlySession(t, ctx, pool)

	if cookie.Value == tokenHash {
		t.Fatal("the cookie carries the stored token_hash — the token and its hash are the wrong way round")
	}
	if auth.HashToken(cookie.Value) != tokenHash {
		t.Errorf("sha256 of the cookie does not match token_hash, so the stored row cannot be found from the cookie")
	}
	if userID != id {
		t.Errorf("user_id = %q, want %q", userID, id)
	}

	// The row's clock and the cookie's have to name the same instant. A minute
	// of slack covers the gap between the handler's time.Now and this read.
	want := time.Now().Add(testSessionTTL)
	if diff := expiresAt.Sub(want); diff > time.Minute || diff < -time.Minute {
		t.Errorf("expires_at is %v from now+TTL, want it within a minute", diff)
	}
	if cookie.MaxAge != int(testSessionTTL.Seconds()) {
		t.Errorf("Max-Age = %d, want %d — the cookie and the row must expire together",
			cookie.MaxAge, int(testSessionTTL.Seconds()))
	}
}

// TestNoLoginResponseCarriesTheHash searches the bytes that actually went out,
// rather than a struct that could only ever hold the fields it was declared
// with.
func TestNoLoginResponseCarriesTheHash(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	seedAccount(t, ctx, pool, testEmail, testPassword, true)

	_, body := postLogin(t, srv.URL, loginBody(testEmail, testPassword))

	if bytes.Contains(body, []byte("$argon2id$")) || bytes.Contains(body, []byte("pass_hash")) {
		t.Errorf("the login response carries the stored hash: %s", body)
	}
	if bytes.Contains(body, []byte(testPassword)) {
		t.Errorf("the login response echoes the password: %s", body)
	}

	tokenHash, _, _ := onlySession(t, ctx, pool)
	if bytes.Contains(body, []byte(tokenHash)) {
		t.Errorf("the login response body carries the session token hash: %s", body)
	}
}

// TestHandleLoginAnswersEveryRefusalIdentically is the requirement from the
// issue, and the reason msgInvalidCredentials is a constant.
//
// The bodies are compared to one another rather than to a literal, so that
// rewording the message keeps this test meaningful instead of failing it.
//
// The deactivated case does not reach handleLogin's Active check: the store
// query filters on active, so the account comes back as ErrNotFound and takes
// the VerifyDummy path. That is the better of the two, because it spends the
// argon2 cost as well as returning the same bytes — but it does mean this case
// is pinning the store's behaviour, not the handler's.
func TestHandleLoginAnswersEveryRefusalIdentically(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	seedAccount(t, ctx, pool, testEmail, testPassword, true)
	seedAccount(t, ctx, pool, "disabled@example.com", testPassword, false)

	cases := []struct {
		name string
		body string
	}{
		{"unknown email", loginBody("nobody@example.com", testPassword)},
		{"wrong password", loginBody(testEmail, "not-the-password")},
		{"deactivated account", loginBody("disabled@example.com", testPassword)},
	}

	var first []byte
	for i, c := range cases {
		res, body := postLogin(t, srv.URL, c.body)

		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want %d — body %s", c.name, res.StatusCode, http.StatusUnauthorized, body)
		}

		if i == 0 {
			first = body
			continue
		}
		if !bytes.Equal(body, first) {
			t.Errorf("%s answered %s, but %s answered %s — the two must be indistinguishable",
				c.name, body, cases[0].name, first)
		}
	}

	if n := countSessions(t, ctx, pool); n != 0 {
		t.Errorf("sessions holds %d rows after three refused logins, want 0", n)
	}
}

// TestHandleLoginWritesNoSessionWhenRefused pairs with the test above: an
// identical body is no use if one of the paths quietly issued a cookie anyway.
func TestHandleLoginWritesNoSessionWhenRefused(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	seedAccount(t, ctx, pool, testEmail, testPassword, true)

	for _, c := range []struct {
		name string
		body string
	}{
		{"unknown email", loginBody("nobody@example.com", testPassword)},
		{"wrong password", loginBody(testEmail, "not-the-password")},
		{"missing fields", loginBody("", "")},
		{"malformed body", `{"email": `},
	} {
		res, body := postLogin(t, srv.URL, c.body)

		if res.StatusCode < 400 {
			t.Errorf("%s: status = %d, want a refusal — body %s", c.name, res.StatusCode, body)
		}
		if hasSessionCookie(res) {
			t.Errorf("%s: a session cookie was set on a refused login", c.name)
		}
		if n := countSessions(t, ctx, pool); n != 0 {
			t.Errorf("%s: sessions holds %d rows, want 0", c.name, n)
		}
	}
}

func TestHandleLoginRefusesAnIncompleteRequest(t *testing.T) {
	srv, _ := newTestServer(t)

	for _, c := range []struct {
		name string
		body string
	}{
		{"no email", loginBody("", testPassword)},
		{"no password", loginBody(testEmail, "")},
		{"whitespace email", loginBody("   ", testPassword)},
	} {
		res, body := postLogin(t, srv.URL, c.body)

		if res.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want %d — body %s",
				c.name, res.StatusCode, http.StatusUnprocessableEntity, body)
		}
	}
}

// TestHandleLoginAcceptsCredentialsRegistrationWouldRefuse pins the deliberate
// asymmetry between loginRequest.validate and createUserRequest.validate. Both
// of these are refused at registration, and both have to reach the 401 here
// rather than a 422: answering them differently sorts inputs into plausible and
// implausible accounts, which is the enumeration this endpoint spends an argon2
// verify to avoid.
func TestHandleLoginAcceptsCredentialsRegistrationWouldRefuse(t *testing.T) {
	srv, _ := newTestServer(t)

	for _, c := range []struct {
		name string
		body string
	}{
		{"password below minPassword", loginBody(testEmail, "short")},
		{"address mail.ParseAddress rejects", loginBody("not-an-address", testPassword)},
	} {
		res, body := postLogin(t, srv.URL, c.body)

		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want %d — body %s",
				c.name, res.StatusCode, http.StatusUnauthorized, body)
		}
	}
}

// TestLoginCookieAttributes pins the attributes that are constants in
// cookies.go, and that Secure is the one taken from configuration.
func TestLoginCookieAttributes(t *testing.T) {
	for _, secure := range []bool{true, false} {
		t.Run(fmt.Sprintf("secure=%v", secure), func(t *testing.T) {
			cfg := config.Config{
				Session: config.SessionConfig{TTL: testSessionTTL, CookieSecure: secure},
			}
			srv, pool := newTestServerWithConfig(t, cfg)
			ctx := testContext(t)

			seedAccount(t, ctx, pool, testEmail, testPassword, true)

			res, _ := postLogin(t, srv.URL, loginBody(testEmail, testPassword))
			cookie := sessionCookieFrom(t, res)

			if !cookie.HttpOnly {
				t.Error("HttpOnly is not set: a script could read the session out of document.cookie")
			}
			if cookie.SameSite != http.SameSiteLaxMode {
				t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
			}
			if cookie.Path != "/" {
				t.Errorf("Path = %q, want \"/\" — anything narrower and the cookie reaches only some routes", cookie.Path)
			}
			if cookie.Secure != secure {
				t.Errorf("Secure = %v, want %v — it has to follow SESSION_COOKIE_SECURE", cookie.Secure, secure)
			}
			if cookie.Domain != "" {
				t.Errorf("Domain = %q, want it absent so the cookie stays host-only", cookie.Domain)
			}
		})
	}
}

// TestHandleLoginMatchesTheEmailCaseInsensitively pins what the store's
// lower(email) = lower($1) implies at this end: an address stored as typed is
// still reachable from a keyboard that capitalised it. Registration does not
// normalise, so without this the casing of the first login would matter.
func TestHandleLoginMatchesTheEmailCaseInsensitively(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	id := seedAccount(t, ctx, pool, testEmail, testPassword, true)

	res, body := postLogin(t, srv.URL, loginBody(strings.ToUpper(testEmail), testPassword))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d — body %s", res.StatusCode, http.StatusOK, body)
	}
	if got := decodeUser(t, body); got.ID != id {
		t.Errorf("id = %q, want %q", got.ID, id)
	}
}

// TestHandleLoginTrimsTheEmail covers the other half: handleCreateUser trims
// before storing, so a leading space typed into a login form has to be trimmed
// here or it matches no row.
func TestHandleLoginTrimsTheEmail(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	seedAccount(t, ctx, pool, testEmail, testPassword, true)

	res, body := postLogin(t, srv.URL, loginBody("  "+testEmail+"  ", testPassword))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d — body %s", res.StatusCode, http.StatusOK, body)
	}
}
