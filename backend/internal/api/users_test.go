package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Vidal322/activity-library/internal/auth"
)

// These tests drive the user endpoints over a real socket through
// newTestServer, so routing, middleware, readJSON and the response encoding are
// all in the path. Assertions are on the wire bytes rather than on a handler's
// return value, because the wire is the contract.

// testPassword is long enough to clear minPassword, so a test about something
// else is never failed by the length rule.
const testPassword = "correct-horse-battery"

// postUser sends a registration and returns the status, the raw body and the
// Location header. The body is returned unparsed: the hash assertion has to
// search the bytes that actually went out, not a struct that could only ever
// hold the fields it was declared with.
func postUser(t *testing.T, baseURL, body string) (int, []byte, string) {
	t.Helper()

	res, err := http.Post(baseURL+"/v1/users", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/users: %v", err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("could not read the response body: %v", err)
	}

	return res.StatusCode, raw, res.Header.Get("Location")
}

// getUser fetches one account and returns the status alongside the body.
func getUser(t *testing.T, baseURL, id string) (int, []byte) {
	t.Helper()

	res, err := authedGet(t, baseURL+"/v1/users/"+id)
	if err != nil {
		t.Fatalf("GET /v1/users/%s: %v", id, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("could not read the response body: %v", err)
	}

	return res.StatusCode, raw
}

// seedUser inserts one account directly and returns its id, for the read tests
// that want a row without going through the endpoint under test.
func seedUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name, email string) string {
	t.Helper()

	var id string
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (name, email, pass_hash) VALUES ($1, $2, 'not-a-real-hash') RETURNING id`,
		name, email,
	).Scan(&id); err != nil {
		t.Fatalf("could not seed an account: %v", err)
	}

	return id
}

// decodeUser parses a response body as the detail shape.
func decodeUser(t *testing.T, body []byte) userDetail {
	t.Helper()

	var got userDetail
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("could not decode the response %s: %v", body, err)
	}

	return got
}

// TestHandleCreateUserReturnsTheAccount pins the happy path: the wire shape,
// the 201, and the standing the new account arrives with. Role and active are
// asserted here as well as in the store tests because this is the surface a
// client sees, and a handler that filled them in from the request would pass
// the store test and fail this one.
func TestHandleCreateUserReturnsTheAccount(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	status, body, location := postUser(t, srv.URL,
		`{"name":"Ana Marques","email":"ana@example.test","password":"`+testPassword+`"}`)

	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d: %s", status, http.StatusCreated, body)
	}

	got := decodeUser(t, body)

	if got.ID == "" {
		t.Error("id is empty, want the uuid the row was given")
	}
	if got.Name != "Ana Marques" {
		t.Errorf("name = %q, want %q", got.Name, "Ana Marques")
	}
	if got.Img != nil {
		t.Errorf("img = %q, want null", *got.Img)
	}
	if got.Role != "outsider" {
		t.Errorf("role = %q, want %q: a registration must not choose its own standing", got.Role, "outsider")
	}
	if !got.Active {
		t.Error("active = false, want true")
	}
	if got.CreatedAt.IsZero() {
		t.Error("created_at is the zero time, want the value the row was given")
	}

	if want := apiPrefix + "/users/" + got.ID; location != want {
		t.Errorf("Location = %q, want %q", location, want)
	}
}

// TestHandleCreateUserLocationResolves is the half of the header that matters.
// A Location that is merely well formed is worth nothing; this follows it and
// requires the same account back, so a prefix that drifts from the route fails
// here rather than in a client.
func TestHandleCreateUserLocationResolves(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	status, body, location := postUser(t, srv.URL,
		`{"name":"Ana Marques","email":"ana@example.test","password":"`+testPassword+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d: %s", status, http.StatusCreated, body)
	}
	created := decodeUser(t, body)

	res, err := authedGet(t, srv.URL+location)
	if err != nil {
		t.Fatalf("GET %s: %v", location, err)
	}
	defer res.Body.Close()

	followed, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("could not read the response body: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want %d: %s", location, res.StatusCode, http.StatusOK, followed)
	}

	if id := decodeUser(t, followed).ID; id != created.ID {
		t.Errorf("Location led to %q, want the created account %q", id, created.ID)
	}
}

// TestHandleCreateUserStoresAUsableHash is the end of the argon2 path: the
// password goes in over HTTP, and what lands in the column has to verify
// against it. A handler that stored the password itself, or stored a hash of
// the wrong string, passes every other test in this file.
func TestHandleCreateUserStoresAUsableHash(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	status, body, _ := postUser(t, srv.URL,
		`{"name":"Ana Marques","email":"ana@example.test","password":"`+testPassword+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d: %s", status, http.StatusCreated, body)
	}

	var stored string
	if err := pool.QueryRow(ctx,
		`SELECT pass_hash FROM users WHERE email = 'ana@example.test'`,
	).Scan(&stored); err != nil {
		t.Fatalf("could not read the stored hash: %v", err)
	}

	if stored == testPassword {
		t.Fatal("the column holds the password itself, want a hash")
	}

	ok, err := auth.Verify(testPassword, stored)
	if err != nil {
		t.Fatalf("the stored hash could not be parsed: %v", err)
	}
	if !ok {
		t.Error("the stored hash does not verify against the password that was sent")
	}

	wrong, err := auth.Verify("not-the-password", stored)
	if err != nil {
		t.Fatalf("verifying a wrong password: %v", err)
	}
	if wrong {
		t.Error("the stored hash verified against the wrong password")
	}
}

// TestNoResponseEverCarriesTheHash is the assertion issue #27 names, and it is
// written against the raw bytes on purpose. Decoding into userDetail and
// finding no hash would prove only that userDetail has no field for one: the
// leak this guards against is a handler that serialises store.User directly, or
// a field added to the response later, and neither is visible through a struct
// that cannot represent them.
//
// Every response the user endpoints can produce is swept, not just the happy
// path, because an error body assembled from a row would leak just as well as a
// successful one.
func TestNoResponseEverCarriesTheHash(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seeded := seedUser(t, ctx, pool, "Bruno Costa", "bruno@example.test")

	// A hash of the real password, to search for alongside the password
	// itself: the encoded form is what a leak would most plausibly contain.
	hash, err := auth.Hash(testPassword)
	if err != nil {
		t.Fatalf("could not hash a password: %v", err)
	}

	var bodies [][]byte
	record := func(body []byte) { bodies = append(bodies, body) }

	// Created, then the conflict, then a refusal, then the reads.
	_, created, _ := postUser(t, srv.URL,
		`{"name":"Ana Marques","email":"ana@example.test","password":"`+testPassword+`"}`)
	record(created)

	_, conflict, _ := postUser(t, srv.URL,
		`{"name":"Ana Again","email":"ana@example.test","password":"`+testPassword+`"}`)
	record(conflict)

	_, invalid, _ := postUser(t, srv.URL,
		`{"name":"Ana","email":"potato","password":"`+testPassword+`"}`)
	record(invalid)

	_, read := getUser(t, srv.URL, decodeUser(t, created).ID)
	record(read)

	_, seededRead := getUser(t, srv.URL, seeded)
	record(seededRead)

	_, missing := getUser(t, srv.URL, "00000000-0000-7000-8000-000000000000")
	record(missing)

	needles := map[string]string{
		"the password":         testPassword,
		"a hash of it":         hash,
		"the seeded hash":      "not-a-real-hash",
		"the column name":      "pass_hash",
		"the Go field name":    "PassHash",
		"the argon2id marker":  "$argon2id$",
		"the registered email": "ana@example.test",
	}

	for _, body := range bodies {
		for label, needle := range needles {
			if bytes.Contains(body, []byte(needle)) {
				t.Errorf("a response body contains %s: %s", label, body)
			}
		}
	}
}

// TestHandleCreateUserRejectsADuplicate is the 409. The address is sent back in
// a different case, so this covers the whole path the partial index on
// lower(email) sits in: the constraint fires, classify names it, and
// conflictMessages turns that name into something a person can act on.
func TestHandleCreateUserRejectsADuplicate(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	status, body, _ := postUser(t, srv.URL,
		`{"name":"Ana Marques","email":"ana@example.test","password":"`+testPassword+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d: %s", status, http.StatusCreated, body)
	}

	status, body, _ = postUser(t, srv.URL,
		`{"name":"Ana Again","email":"ANA@Example.TEST","password":"`+testPassword+`"}`)

	if status != http.StatusConflict {
		t.Fatalf("status = %d, want %d: %s", status, http.StatusConflict, body)
	}
	if want := "that email is already registered"; !strings.Contains(string(body), want) {
		t.Errorf("body = %s, want it to name the collision: %q", body, want)
	}
}

// TestHandleCreateUserRefusesInvalidValues covers the 422s. The split from the
// 400s below is the point: these bodies parsed, so the request was understood
// and refused, which is the same answer a check constraint would produce.
func TestHandleCreateUserRefusesInvalidValues(t *testing.T) {
	longName := strings.Repeat("a", maxName+1)
	longEmail := strings.Repeat("a", maxEmail) + "@example.test"

	for _, tc := range []struct {
		label, body, wantMsg string
	}{
		{
			"missing name",
			`{"email":"ana@example.test","password":"` + testPassword + `"}`,
			"required",
		},
		{
			"name of spaces",
			`{"name":"   ","email":"ana@example.test","password":"` + testPassword + `"}`,
			"required",
		},
		{
			"missing password",
			`{"name":"Ana Marques","email":"ana@example.test"}`,
			"required",
		},
		{
			"email with no domain",
			`{"name":"Ana Marques","email":"potato","password":"` + testPassword + `"}`,
			"valid address",
		},
		{
			// ParseAddress accepts this; the handler must not, or the display
			// name would end up in the column.
			"email in mailbox form",
			`{"name":"Ana Marques","email":"Ana <ana@example.test>","password":"` + testPassword + `"}`,
			"valid address",
		},
		{
			"short password",
			`{"name":"Ana Marques","email":"ana@example.test","password":"short"}`,
			"at least",
		},
		{
			"name too long",
			`{"name":"` + longName + `","email":"ana@example.test","password":"` + testPassword + `"}`,
			"longer than",
		},
		{
			"email too long",
			`{"name":"Ana Marques","email":"` + longEmail + `","password":"` + testPassword + `"}`,
			"longer than",
		},
	} {
		t.Run(tc.label, func(t *testing.T) {
			srv, _ := newAuthedTestServer(t)

			status, body, _ := postUser(t, srv.URL, tc.body)

			if status != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want %d: %s", status, http.StatusUnprocessableEntity, body)
			}
			if !strings.Contains(string(body), tc.wantMsg) {
				t.Errorf("body = %s, want it to mention %q", body, tc.wantMsg)
			}
		})
	}
}

// TestHandleCreateUserRejectsAnUnreadableBody is the other side of that split:
// nothing here parsed, so nothing could be refused on its values.
//
// The role case is the one with teeth. DisallowUnknownFields is what stops a
// request from naming a column the handler never passes on, so a request for
// admin is turned away rather than quietly ignored, and the refusal is visible
// if that decoder setting is ever relaxed.
func TestHandleCreateUserRejectsAnUnreadableBody(t *testing.T) {
	for _, tc := range []struct {
		label, body, wantMsg string
	}{
		{"empty", ``, "must not be empty"},
		{"null", `null`, "must not be null"},
		{"malformed", `{"name":`, "malformed"},
		{"not an object", `"ana"`, "invalid value"},
		{"wrong field type", `{"name":42,"email":"ana@example.test","password":"` + testPassword + `"}`, "invalid value"},
		{"two values", `{"name":"Ana","email":"ana@example.test","password":"` + testPassword + `"}{}`, "single JSON value"},
		{
			"a role it may not choose",
			`{"name":"Ana Marques","email":"ana@example.test","password":"` + testPassword + `","role":"admin"}`,
			"unknown field",
		},
	} {
		t.Run(tc.label, func(t *testing.T) {
			srv, _ := newAuthedTestServer(t)

			status, body, _ := postUser(t, srv.URL, tc.body)

			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d: %s", status, http.StatusBadRequest, body)
			}
			if !strings.Contains(string(body), tc.wantMsg) {
				t.Errorf("body = %s, want it to mention %q", body, tc.wantMsg)
			}
		})
	}
}

// TestHandleCreateUserWritesNothingWhenRefused checks that a refusal leaves the
// table as it found it. The validation runs before the insert, so this is the
// assertion that a later reordering would break.
func TestHandleCreateUserWritesNothingWhenRefused(t *testing.T) {
	// Not newAuthedTestServer: registration is open, and the count below is
	// over the whole table, so a seeded session account would fail it.
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	status, body, _ := postUser(t, srv.URL,
		`{"name":"Ana Marques","email":"potato","password":"`+testPassword+`"}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d: %s", status, http.StatusUnprocessableEntity, body)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
		t.Fatalf("could not count the accounts: %v", err)
	}
	if count != 0 {
		t.Errorf("the table holds %d accounts, want none after a refusal", count)
	}
}

// TestHandleGetUserReturnsTheAccount pins the read shape, which the create
// endpoint reuses.
func TestHandleGetUserReturnsTheAccount(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	// A second account the request must not return.
	seedUser(t, ctx, pool, "Bruno Costa", "bruno@example.test")
	wanted := seedUser(t, ctx, pool, "Ana Marques", "ana@example.test")

	status, body := getUser(t, srv.URL, wanted)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", status, http.StatusOK, body)
	}

	got := decodeUser(t, body)
	if got.ID != wanted {
		t.Errorf("id = %q, want %q", got.ID, wanted)
	}
	if got.Name != "Ana Marques" {
		t.Errorf("name = %q, want %q", got.Name, "Ana Marques")
	}
}

// TestHandleGetUserRejectsABadID covers the two ways a read fails: an id that
// is not a uuid never reaches the database, and one that is reaches it and
// matches nothing. They are different statuses because they are different
// mistakes.
func TestHandleGetUserRejectsABadID(t *testing.T) {
	for _, tc := range []struct {
		label, id  string
		wantStatus int
	}{
		{"malformed", "not-a-uuid", http.StatusBadRequest},
		{"unknown", "00000000-0000-7000-8000-000000000000", http.StatusNotFound},
	} {
		t.Run(tc.label, func(t *testing.T) {
			srv, _ := newAuthedTestServer(t)

			status, body := getUser(t, srv.URL, tc.id)
			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d: %s", status, tc.wantStatus, body)
			}
		})
	}
}
