package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Vidal322/activity-library/internal/auth"
)

// These tests drive the invitation endpoints over a real socket. Creating and
// revoking sit behind requireAdmin, so most of them promote the seeded session
// account first; looking up and accepting are public, and are called without a
// cookie to prove it.

// newAdminTestServer is newAuthedTestServer with the session account promoted
// to admin.
func newAdminTestServer(t *testing.T) (string, *pgxpool.Pool) {
	t.Helper()

	srv, pool := newAuthedTestServer(t)

	if _, err := pool.Exec(testContext(t),
		`UPDATE users SET role = 'admin' WHERE id = $1`, testSessionUserID,
	); err != nil {
		t.Fatalf("could not promote the session account: %v", err)
	}

	return srv.URL, pool
}

// send performs one request and returns the status and the raw body. A
// session cookie is added only when authed is true, so the public routes are
// exercised the way an invitee with no account reaches them.
func send(t *testing.T, method, url, body string, authed bool) (int, []byte) {
	t.Helper()

	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("could not build %s %s: %v", method, url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if authed {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: testSessionToken})
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

	return res.StatusCode, raw
}

// invite creates an invitation through the endpoint and returns the response,
// which is the only place the raw token can be read.
func invite(t *testing.T, baseURL, email string) createdInvitation {
	t.Helper()

	status, body := send(t, http.MethodPost, baseURL+"/v1/invitations", `{"email":"`+email+`"}`, true)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d — body %s", status, http.StatusCreated, body)
	}

	var got createdInvitation
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("could not decode the response %s: %v", body, err)
	}

	return got
}

func acceptBody(token string) string {
	return `{"token":"` + token + `","name":"New Person","password":"` + testPassword + `"}`
}

func TestHandleCreateInvitationReturnsTheToken(t *testing.T) {
	baseURL, pool := newAdminTestServer(t)

	got := invite(t, baseURL, "  new@example.test ")

	if got.Email != "new@example.test" {
		t.Errorf("Email = %q, want the trimmed address", got.Email)
	}
	if got.InvitedBy != testSessionUserID {
		t.Errorf("InvitedBy = %q, want the session account %q", got.InvitedBy, testSessionUserID)
	}
	if got.ExpiresAt.Sub(got.CreatedAt).Round(time.Minute) != testInvitationTTL {
		t.Errorf("ExpiresAt - CreatedAt = %v, want the configured %v", got.ExpiresAt.Sub(got.CreatedAt), testInvitationTTL)
	}

	// The token in the response must be the one whose hash was stored, and the
	// hash itself must never reach the wire.
	var stored string
	if err := pool.QueryRow(testContext(t),
		`SELECT token_hash FROM invitations WHERE id = $1`, got.ID,
	).Scan(&stored); err != nil {
		t.Fatalf("could not read the invitation back: %v", err)
	}
	if stored != auth.HashToken(got.Token) {
		t.Error("the stored hash is not the hash of the returned token")
	}
}

func TestNoInvitationResponseCarriesTheHash(t *testing.T) {
	baseURL, pool := newAdminTestServer(t)

	created := invite(t, baseURL, "new@example.test")

	var hash string
	if err := pool.QueryRow(testContext(t),
		`SELECT token_hash FROM invitations WHERE id = $1`, created.ID,
	).Scan(&hash); err != nil {
		t.Fatalf("could not read the invitation back: %v", err)
	}

	_, lookup := send(t, http.MethodPost, baseURL+"/v1/invitations/lookup", `{"token":"`+created.Token+`"}`, false)
	_, revoke := send(t, http.MethodPost, baseURL+"/v1/invitations/"+created.ID+"/revoke", ``, true)

	for name, body := range map[string][]byte{"lookup": lookup, "revoke": revoke} {
		if strings.Contains(string(body), hash) {
			t.Errorf("the %s response carries the token hash: %s", name, body)
		}
	}
}

func TestHandleCreateInvitationIsForAdmins(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	status, body := send(t, http.MethodPost, srv.URL+"/v1/invitations", `{"email":"new@example.test"}`, true)
	if status != http.StatusForbidden {
		t.Fatalf("member: status = %d, want %d — body %s", status, http.StatusForbidden, body)
	}
	assertErrorMessage(t, body, msgNotAdmin)

	status, body = send(t, http.MethodPost, srv.URL+"/v1/invitations", `{"email":"new@example.test"}`, false)
	if status != http.StatusUnauthorized {
		t.Fatalf("anonymous: status = %d, want %d — body %s", status, http.StatusUnauthorized, body)
	}
}

func TestHandleCreateInvitationRefusesABadEmail(t *testing.T) {
	baseURL, _ := newAdminTestServer(t)

	tests := []struct {
		body string
		want string
	}{
		{`{}`, "email is required"},
		{`{"email":"   "}`, "email is required"},
		{`{"email":"not an email"}`, "email is not a valid address"},
		{`{"email":"Name <new@example.test>"}`, "email is not a valid address"},
	}

	for _, tt := range tests {
		status, body := send(t, http.MethodPost, baseURL+"/v1/invitations", tt.body, true)
		if status != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want %d — body %s", tt.body, status, http.StatusUnprocessableEntity, body)
			continue
		}
		assertErrorMessage(t, body, tt.want)
	}
}

func TestHandleCreateInvitationRefusesASecondOpenInvitation(t *testing.T) {
	baseURL, _ := newAdminTestServer(t)

	invite(t, baseURL, "new@example.test")

	status, body := send(t, http.MethodPost, baseURL+"/v1/invitations", `{"email":"NEW@example.test"}`, true)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want %d — body %s", status, http.StatusConflict, body)
	}
	assertErrorMessage(t, body, "that email already has an open invitation")
}

// TestHandleLookupInvitationIsPublicAndMinimal pins both halves of the route's
// contract: no session is needed, and nothing beyond the address and expiry is
// given to whoever holds the link.
func TestHandleLookupInvitationIsPublicAndMinimal(t *testing.T) {
	baseURL, _ := newAdminTestServer(t)

	created := invite(t, baseURL, "new@example.test")

	status, body := send(t, http.MethodPost, baseURL+"/v1/invitations/lookup", `{"token":"`+created.Token+`"}`, false)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d — body %s", status, http.StatusOK, body)
	}

	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("could not decode the response %s: %v", body, err)
	}
	if got["email"] != "new@example.test" {
		t.Errorf("email = %v, want %q", got["email"], "new@example.test")
	}
	for key := range got {
		if key != "email" && key != "expires_at" {
			t.Errorf("the public preview carries %q, want only email and expires_at", key)
		}
	}
}

func TestHandleLookupInvitationRefuses(t *testing.T) {
	baseURL, _ := newAdminTestServer(t)

	tests := []struct {
		name   string
		body   string
		status int
	}{
		{"unknown token", `{"token":"never-issued"}`, http.StatusNotFound},
		{"no token", `{}`, http.StatusUnprocessableEntity},
		{"malformed body", `not json`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		status, body := send(t, http.MethodPost, baseURL+"/v1/invitations/lookup", tt.body, false)
		if status != tt.status {
			t.Errorf("%s: status = %d, want %d — body %s", tt.name, status, tt.status, body)
		}
	}
}

func TestHandleAcceptInvitationCreatesAMember(t *testing.T) {
	baseURL, _ := newAdminTestServer(t)

	created := invite(t, baseURL, "new@example.test")

	status, body := send(t, http.MethodPost, baseURL+"/v1/invitations/accept", acceptBody(created.Token), false)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d — body %s", status, http.StatusCreated, body)
	}

	got := decodeUser(t, body)
	if got.Name != "New Person" {
		t.Errorf("Name = %q, want %q", got.Name, "New Person")
	}
	if got.Role != "member" {
		t.Errorf("Role = %q, want member", got.Role)
	}

	// The account has to be usable: the password given at accept time logs in
	// at the invited address.
	login(t, baseURL, "new@example.test", testPassword)
}

func TestHandleAcceptInvitationOnlyOnce(t *testing.T) {
	baseURL, _ := newAdminTestServer(t)

	created := invite(t, baseURL, "new@example.test")

	if status, body := send(t, http.MethodPost, baseURL+"/v1/invitations/accept", acceptBody(created.Token), false); status != http.StatusCreated {
		t.Fatalf("first accept: status = %d, want %d — body %s", status, http.StatusCreated, body)
	}

	status, body := send(t, http.MethodPost, baseURL+"/v1/invitations/accept", acceptBody(created.Token), false)
	if status != http.StatusNotFound {
		t.Fatalf("second accept: status = %d, want %d — body %s", status, http.StatusNotFound, body)
	}
}

func TestHandleAcceptInvitationRefusesABadRequest(t *testing.T) {
	baseURL, _ := newAdminTestServer(t)

	created := invite(t, baseURL, "new@example.test")

	tests := []struct {
		name string
		body string
		want string
	}{
		{"no token", `{"name":"New Person","password":"` + testPassword + `"}`, "token, name and password are required"},
		{"blank name", `{"token":"` + created.Token + `","name":"  ","password":"` + testPassword + `"}`, "token, name and password are required"},
		{"short password", `{"token":"` + created.Token + `","name":"New Person","password":"short"}`, "password must be at least 8 characters"},
	}

	for _, tt := range tests {
		status, body := send(t, http.MethodPost, baseURL+"/v1/invitations/accept", tt.body, false)
		if status != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want %d — body %s", tt.name, status, http.StatusUnprocessableEntity, body)
			continue
		}
		assertErrorMessage(t, body, tt.want)
	}

	// None of the refusals may have used the invitation up.
	status, body := send(t, http.MethodPost, baseURL+"/v1/invitations/lookup", `{"token":"`+created.Token+`"}`, false)
	if status != http.StatusOK {
		t.Fatalf("the invitation stopped working after refused accepts: status = %d — body %s", status, body)
	}
}

func TestHandleRevokeInvitationRecordsTheAdmin(t *testing.T) {
	baseURL, _ := newAdminTestServer(t)

	created := invite(t, baseURL, "new@example.test")

	status, body := send(t, http.MethodPost, baseURL+"/v1/invitations/"+created.ID+"/revoke", ``, true)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d — body %s", status, http.StatusOK, body)
	}

	var got invitationDetail
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("could not decode the response %s: %v", body, err)
	}
	if got.RevokedBy == nil || *got.RevokedBy != testSessionUserID {
		t.Errorf("RevokedBy = %v, want the session account %q", got.RevokedBy, testSessionUserID)
	}
	if got.RevokedAt == nil {
		t.Error("RevokedAt is nil, want the time of revocation")
	}

	// A revoked link must stop working for the invitee.
	status, body = send(t, http.MethodPost, baseURL+"/v1/invitations/accept", acceptBody(created.Token), false)
	if status != http.StatusNotFound {
		t.Fatalf("accept after revoke: status = %d, want %d — body %s", status, http.StatusNotFound, body)
	}
}

func TestHandleRevokeInvitationRefuses(t *testing.T) {
	baseURL, _ := newAdminTestServer(t)

	created := invite(t, baseURL, "new@example.test")
	if status, body := send(t, http.MethodPost, baseURL+"/v1/invitations/"+created.ID+"/revoke", ``, true); status != http.StatusOK {
		t.Fatalf("first revoke: status = %d — body %s", status, body)
	}

	tests := []struct {
		name   string
		id     string
		status int
	}{
		{"already revoked", created.ID, http.StatusNotFound},
		{"unknown id", "10000000-0000-7000-8000-00000000dead", http.StatusNotFound},
		{"malformed id", "not-a-uuid", http.StatusBadRequest},
	}

	for _, tt := range tests {
		status, body := send(t, http.MethodPost, baseURL+"/v1/invitations/"+tt.id+"/revoke", ``, true)
		if status != tt.status {
			t.Errorf("%s: status = %d, want %d — body %s", tt.name, status, tt.status, body)
		}
	}
}

func TestHandleRevokeInvitationIsForAdmins(t *testing.T) {
	baseURL, pool := newAdminTestServer(t)

	created := invite(t, baseURL, "new@example.test")

	if _, err := pool.Exec(testContext(t),
		`UPDATE users SET role = 'member' WHERE id = $1`, testSessionUserID,
	); err != nil {
		t.Fatalf("could not demote the session account: %v", err)
	}

	status, body := send(t, http.MethodPost, baseURL+"/v1/invitations/"+created.ID+"/revoke", ``, true)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d — body %s", status, http.StatusForbidden, body)
	}
}
