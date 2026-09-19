package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Vidal322/activity-library/internal/auth"
)

// These tests drive requireAuth through GET /v1/auth/me, the one route that
// exists to say who the caller is. The middleware is the subject; handleMe is
// only the thing on the other side of it, which is why the assertions are
// about status, cookies and the sessions table rather than the response shape.

// getMe sends a request carrying the cookie it is given, or no cookie when that
// is nil. Like postLogout, it builds the request by hand rather than using a
// client with a jar: a jar would silently decline to send a cookie whose flags
// did not match, and the test would pass without the middleware ever running.
func getMe(t *testing.T, baseURL string, cookie *http.Cookie) (*http.Response, []byte) {
	t.Helper()

	return doWithCookie(t, http.MethodGet, baseURL+"/v1/auth/me", cookie)
}

func doWithCookie(t *testing.T, method, url string, cookie *http.Cookie) (*http.Response, []byte) {
	t.Helper()

	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("could not build the %s request: %v", method, err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("could not read the response body: %v", err)
	}

	return res, raw
}

// insertSession writes a session row directly, so a test can produce the states
// login cannot: one that has already expired, and one that has been revoked.
func insertSession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID, token string, expiresAt time.Time, revoked bool) *http.Cookie {
	t.Helper()

	var revokedAt *time.Time
	if revoked {
		now := time.Now()
		revokedAt = &now
	}

	_, err := pool.Exec(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at, revoked_at) VALUES ($1, $2, $3, $4)`,
		auth.HashToken(token), userID, expiresAt, revokedAt,
	)
	if err != nil {
		t.Fatalf("could not insert a session: %v", err)
	}

	return &http.Cookie{Name: sessionCookieName, Value: token}
}

// TestRequireAuthAdmitsALiveSession is the issue's first half: the login cookie
// reaches an authenticated endpoint. Two accounts are seeded so the test also
// pins that the handler sees the caller rather than just some user — a
// middleware that put the wrong id on the context would pass with one.
func TestRequireAuthAdmitsALiveSession(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	seedUser(t, ctx, pool, "Someone Else", "else@example.com")
	id := seedAccount(t, ctx, pool, testEmail, testPassword, true)

	cookie := login(t, srv.URL, testEmail, testPassword)

	res, body := getMe(t, srv.URL, cookie)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d — body %s", res.StatusCode, http.StatusOK, body)
	}
	if got := decodeUser(t, body); got.ID != id {
		t.Errorf("id = %q, want %q — the context carried the wrong user", got.ID, id)
	}
}

// TestMeCarriesNoSecret searches the bytes that actually went out. handleMe
// reuses newUserDetail, so this is really pinning that the shape stays the one
// the other endpoints return.
func TestMeCarriesNoSecret(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	seedAccount(t, ctx, pool, testEmail, testPassword, true)
	cookie := login(t, srv.URL, testEmail, testPassword)

	_, body := getMe(t, srv.URL, cookie)

	for _, secret := range []string{"$argon2id$", "pass_hash", testPassword, testEmail, cookie.Value} {
		if bytes.Contains(body, []byte(secret)) {
			t.Errorf("the /me response carries %q: %s", secret, body)
		}
	}

	tokenHash, _, _ := onlySession(t, ctx, pool)
	if bytes.Contains(body, []byte(tokenHash)) {
		t.Errorf("the /me response carries the session token hash: %s", body)
	}
}

// TestRequireAuthRefusesEveryBadSessionIdentically is the issue's second half,
// widened. Every way of arriving without a usable session has to answer the
// same, or the difference tells a prober which token hashes exist.
//
// The expired and revoked cases are the reason this matters beyond the obvious:
// both rows exist and both match on token_hash, and only getSessionQuery's
// WHERE clause excludes them. A hand-rolled time check in Go would pass the
// no-cookie case and fail these.
func TestRequireAuthRefusesEveryBadSessionIdentically(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	userID := seedUser(t, ctx, pool, "Pedro", testEmail)

	unissued, err := auth.NewSessionToken()
	if err != nil {
		t.Fatalf("could not build an unissued token: %v", err)
	}

	expired := insertSession(t, ctx, pool, userID, "expired-token", time.Now().Add(-time.Hour), false)
	revoked := insertSession(t, ctx, pool, userID, "revoked-token", time.Now().Add(testSessionTTL), true)

	cases := []struct {
		name   string
		cookie *http.Cookie
	}{
		{"no cookie", nil},
		{"malformed value", &http.Cookie{Name: sessionCookieName, Value: "not-a-real-token"}},
		{"well-formed but unissued", &http.Cookie{Name: sessionCookieName, Value: unissued}},
		{"expired session", expired},
		{"revoked session", revoked},
	}

	var first []byte
	for i, c := range cases {
		res, body := getMe(t, srv.URL, c.cookie)

		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want %d — body %s",
				c.name, res.StatusCode, http.StatusUnauthorized, body)
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
}

// TestRequireAuthRefusesAfterLogout is the case the issue names, and the one
// that ties #29 to #30: it passes only because logout deletes the row rather
// than merely clearing the cookie.
func TestRequireAuthRefusesAfterLogout(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	seedAccount(t, ctx, pool, testEmail, testPassword, true)
	cookie := login(t, srv.URL, testEmail, testPassword)

	if res, body := getMe(t, srv.URL, cookie); res.StatusCode != http.StatusOK {
		t.Fatalf("before logout: status = %d, want %d — body %s", res.StatusCode, http.StatusOK, body)
	}

	if res, body := postLogout(t, srv.URL, cookie); res.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: status = %d, want %d — body %s", res.StatusCode, http.StatusNoContent, body)
	}

	res, body := getMe(t, srv.URL, cookie)
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("after logout: status = %d, want %d — body %s",
			res.StatusCode, http.StatusUnauthorized, body)
	}
}

// TestRequireAuthClearsACookieItRefuses pins the half a browser acts on: a
// cookie naming a session that no longer exists is dead weight, and leaving it
// in place means the client resends it on every subsequent request.
//
// The no-cookie case is the deliberate exception — there is nothing to clear.
func TestRequireAuthClearsACookieItRefuses(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	userID := seedUser(t, ctx, pool, "Pedro", testEmail)
	expired := insertSession(t, ctx, pool, userID, "expired-token", time.Now().Add(-time.Hour), false)

	res, _ := getMe(t, srv.URL, expired)
	cleared := sessionCookieFrom(t, res)

	if cleared.Value != "" {
		t.Errorf("the cleared cookie still carries a value: %q", cleared.Value)
	}
	if cleared.MaxAge >= 0 {
		t.Errorf("Max-Age = %d, want it negative so the browser drops the cookie now", cleared.MaxAge)
	}

	res, _ = getMe(t, srv.URL, nil)
	if hasSessionCookie(res) {
		t.Error("a Set-Cookie was sent to a caller that presented no cookie")
	}
}

// TestRequireAuthLeavesTheSessionAlone is the guard against a middleware that
// looks right and quietly consumes or rotates the session on every read. Every
// test above this one passes if requireAuth deletes the row after admitting it;
// only a second request notices.
func TestRequireAuthLeavesTheSessionAlone(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	seedAccount(t, ctx, pool, testEmail, testPassword, true)
	cookie := login(t, srv.URL, testEmail, testPassword)

	hashBefore, _, expiresBefore := onlySession(t, ctx, pool)

	for i := range 3 {
		if res, body := getMe(t, srv.URL, cookie); res.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status = %d, want %d — body %s",
				i+1, res.StatusCode, http.StatusOK, body)
		}
	}

	hashAfter, _, expiresAfter := onlySession(t, ctx, pool)

	if hashAfter != hashBefore {
		t.Error("the session token was rotated by a read")
	}
	if !expiresAfter.Equal(expiresBefore) {
		t.Errorf("expires_at moved from %v to %v — the session is being extended on use",
			expiresBefore, expiresAfter)
	}
}

// TestOpenRoutesNeedNoSession pins the other side of the wiring. Registration
// and login must stay reachable without a cookie, or there is no way to obtain
// one; this is the test that fails if either is ever moved inside the group.
func TestOpenRoutesNeedNoSession(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	status, body, _ := postUser(t, srv.URL,
		`{"name":"Ana Marques","email":"ana@example.com","password":"`+testPassword+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("POST /v1/users: status = %d, want %d — body %s", status, http.StatusCreated, body)
	}

	seedAccount(t, ctx, pool, testEmail, testPassword, true)

	res, loginRes := postLogin(t, srv.URL, loginBody(testEmail, testPassword))
	if res.StatusCode != http.StatusOK {
		t.Errorf("POST /v1/auth/login: status = %d, want %d — body %s",
			res.StatusCode, http.StatusOK, loginRes)
	}
}
