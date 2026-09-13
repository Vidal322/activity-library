package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Vidal322/activity-library/internal/store"
)

// These tests cover store.GetUserByID. They share TestMain, queryTimeout and
// usersTest with users_schema_test.go, which pins the column constraints the
// fixtures below rely on.

// insertUser writes one account and returns its id. It takes img as *string so
// a caller can pin the nullable column from either side.
func insertUser(t *testing.T, pool *pgxpool.Pool, ctx context.Context, name, email, role string, img *string) string {
	t.Helper()

	var id string
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (name, email, pass_hash, img, role)
			VALUES ($1, $2, 'not-a-real-hash', $3, $4)
			RETURNING id`,
		name, email, img, role,
	).Scan(&id); err != nil {
		t.Fatalf("could not insert an account: %v", err)
	}

	return id
}

// TestGetUserByIDReturnsEveryColumn is the test the struct tags exist for.
// RowToStructByName matches on names, so a missing or misspelled db tag is
// invisible at compile time and shows up only as a scan that fails or a field
// left at its zero value. Asserting every column means a tag that drifts from
// the SELECT fails here rather than in whatever handler reads it next.
func TestGetUserByIDReturnsEveryColumn(t *testing.T) {
	pool, ctx := usersTest(t)

	img := "https://example.test/ana.png"
	id := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "member", &img)

	user, err := store.GetUserByID(ctx, pool, id)
	if err != nil {
		t.Fatalf("could not read the account back: %v", err)
	}

	if user.ID != id {
		t.Errorf("ID = %q, want %q", user.ID, id)
	}
	if user.Name != "Ana Marques" {
		t.Errorf("Name = %q, want %q", user.Name, "Ana Marques")
	}
	if user.Email != "ana@example.test" {
		t.Errorf("Email = %q, want %q", user.Email, "ana@example.test")
	}
	if user.PassHash != "not-a-real-hash" {
		t.Errorf("PassHash = %q, want %q", user.PassHash, "not-a-real-hash")
	}
	if user.Img == nil {
		t.Errorf("Img = nil, want %q", img)
	} else if *user.Img != img {
		t.Errorf("Img = %q, want %q", *user.Img, img)
	}
	if user.Role != "member" {
		t.Errorf("Role = %q, want %q", user.Role, "member")
	}
	if !user.Active {
		t.Error("Active = false, want true")
	}
	if user.CreatedAt.IsZero() {
		t.Error("CreatedAt is the zero time, want the value the column defaulted to")
	}
	if user.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is the zero time, want the value the column defaulted to")
	}
}

// TestGetUserByIDReadsANullImg pins the reason User.Img is *string. img is the
// one nullable column in the table, and scanning its NULL into a plain string
// is an error, not a zero value: the account with no picture is the common
// case, so getting this wrong would break most reads rather than an edge.
func TestGetUserByIDReadsANullImg(t *testing.T) {
	pool, ctx := usersTest(t)

	id := insertUser(t, pool, ctx, "Bruno Costa", "bruno@example.test", "outsider", nil)

	user, err := store.GetUserByID(ctx, pool, id)
	if err != nil {
		t.Fatalf("could not read an account with no picture: %v", err)
	}

	if user.Img != nil {
		t.Errorf("Img = %q, want nil", *user.Img)
	}
}

// TestGetUserByIDNotFound pins the mapping the handler's 404 depends on. pgx
// reports no rows as its own sentinel; classify turns that into ErrNotFound,
// and storeErrorResponse keys on that sentinel rather than on pgx's.
func TestGetUserByIDNotFound(t *testing.T) {
	pool, ctx := usersTest(t)

	// A syntactically valid uuid that no row carries.
	const missing = "00000000-0000-7000-8000-000000000000"

	_, err := store.GetUserByID(ctx, pool, missing)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("error = %v, want store.ErrNotFound", err)
	}
}

// TestGetUserByIDFindsTheRightAccount guards against a WHERE clause that reads
// the wrong row: with one account in the table, a query matching everything
// passes just as well as one matching on id.
func TestGetUserByIDFindsTheRightAccount(t *testing.T) {
	pool, ctx := usersTest(t)

	wanted := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "member", nil)
	insertUser(t, pool, ctx, "Bruno Costa", "bruno@example.test", "admin", nil)

	user, err := store.GetUserByID(ctx, pool, wanted)
	if err != nil {
		t.Fatalf("could not read the account back: %v", err)
	}

	if user.ID != wanted {
		t.Errorf("ID = %q, want %q", user.ID, wanted)
	}
	if user.Email != "ana@example.test" {
		t.Errorf("Email = %q, want %q", user.Email, "ana@example.test")
	}
}

// TestGetUserByIDReturnsDeletedAccounts records a decision rather than a
// guarantee. users_email_key is partial on active, so a deactivated row is the
// model's deleted account, and this lookup still returns it. Login must not
// accept those, so GetUserByEmail will need `AND active` that this one does
// not; keeping the difference visible is the point of the test. If the public
// endpoint should hide them, this is the test that changes.
func TestGetUserByIDReturnsDeletedAccounts(t *testing.T) {
	pool, ctx := usersTest(t)

	id := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "member", nil)

	if _, err := pool.Exec(ctx, `UPDATE users SET active = false WHERE id = $1`, id); err != nil {
		t.Fatalf("could not delete the account: %v", err)
	}

	user, err := store.GetUserByID(ctx, pool, id)
	if err != nil {
		t.Fatalf("a deleted account was not returned: %v", err)
	}

	if user.Active {
		t.Error("Active = true, want false")
	}
}

// TestGetUserByIDRejectsAMalformedID pins where uuid validation does not
// happen. The store hands the argument to Postgres, which rejects it as a
// malformed literal, and classify has no branch for that: it escapes as a raw
// error and storeErrorResponse turns it into a 500. That is why handleGetUser
// parses the path parameter itself before calling in. The assertion is
// deliberately only that it fails, since the 500 is a consequence of the
// handler's guard, not a behaviour worth pinning.
func TestGetUserByIDRejectsAMalformedID(t *testing.T) {
	pool, ctx := usersTest(t)

	if _, err := store.GetUserByID(ctx, pool, "not-a-uuid"); err == nil {
		t.Fatal("a malformed id was accepted, want an error")
	}
}

// The tests below cover store.GetUserByEmail, the lookup login is built on. It
// differs from GetUserByID in two ways that are easy to lose in a copy-paste,
// and each has a test here: it folds case, and it refuses deleted accounts.

// TestGetUserByEmailFoldsCase is the assertion issue #25 names. users_email_key
// is on lower(email), so a lookup that compares the column directly disagrees
// with the index that enforced uniqueness in the first place: the address that
// was refused at signup as a duplicate would be unfindable at login.
func TestGetUserByEmailFoldsCase(t *testing.T) {
	pool, ctx := usersTest(t)

	id := insertUser(t, pool, ctx, "Ana Marques", "Ana.Marques@Example.test", "member", nil)

	for _, typed := range []string{
		"Ana.Marques@Example.test",
		"ana.marques@example.test",
		"ANA.MARQUES@EXAMPLE.TEST",
		"aNa.MaRqUeS@eXaMpLe.TeSt",
	} {
		user, err := store.GetUserByEmail(ctx, pool, typed)
		if err != nil {
			t.Errorf("looking up %q: %v", typed, err)
			continue
		}
		if user.ID != id {
			t.Errorf("looking up %q: ID = %q, want %q", typed, user.ID, id)
		}
	}
}

// TestGetUserByEmailReturnsThePassHash pins the reason this lookup exists.
// Verifying the hash is the caller's whole purpose, so a query that stopped
// selecting the column would break login while every other field still read
// correctly.
func TestGetUserByEmailReturnsThePassHash(t *testing.T) {
	pool, ctx := usersTest(t)

	insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "member", nil)

	user, err := store.GetUserByEmail(ctx, pool, "ana@example.test")
	if err != nil {
		t.Fatalf("could not read the account back: %v", err)
	}

	if user.PassHash != "not-a-real-hash" {
		t.Errorf("PassHash = %q, want %q", user.PassHash, "not-a-real-hash")
	}
}

// TestGetUserByEmailIgnoresDeletedAccounts is the AND active clause, and the
// one place the two lookups deliberately disagree: GetUserByID still returns a
// deleted account, this one must not. Without the clause a deactivated person
// could still sign in, which is the entire meaning of deactivating them.
func TestGetUserByEmailIgnoresDeletedAccounts(t *testing.T) {
	pool, ctx := usersTest(t)

	id := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "member", nil)

	if _, err := pool.Exec(ctx, `UPDATE users SET active = false WHERE id = $1`, id); err != nil {
		t.Fatalf("could not delete the account: %v", err)
	}

	_, err := store.GetUserByEmail(ctx, pool, "ana@example.test")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("error = %v, want store.ErrNotFound", err)
	}
}

// TestGetUserByEmailFindsTheLiveAccount is the case the partial index creates
// and the one most likely to break in production rather than in a fixture.
// users_email_key is partial on active, so a deleted account keeps its address
// and the person can register again: the table then holds two rows for one
// email, and only the live one may be returned. A lookup missing AND active
// would match both and CollectExactlyOneRow would fail with ErrTooManyRows,
// which classify does not map, so login would 500 rather than 401.
func TestGetUserByEmailFindsTheLiveAccount(t *testing.T) {
	pool, ctx := usersTest(t)

	old := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "member", nil)
	if _, err := pool.Exec(ctx, `UPDATE users SET active = false WHERE id = $1`, old); err != nil {
		t.Fatalf("could not delete the first account: %v", err)
	}

	current := insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "outsider", nil)

	user, err := store.GetUserByEmail(ctx, pool, "ana@example.test")
	if err != nil {
		t.Fatalf("could not read the live account back: %v", err)
	}

	if user.ID != current {
		t.Errorf("ID = %q, want the live account %q", user.ID, current)
	}
}

// TestGetUserByEmailNotFound pins the mapping login's 401 depends on: an
// address nobody registered has to arrive as ErrNotFound, not as a raw error
// the handler would turn into a 500.
func TestGetUserByEmailNotFound(t *testing.T) {
	pool, ctx := usersTest(t)

	insertUser(t, pool, ctx, "Ana Marques", "ana@example.test", "member", nil)

	_, err := store.GetUserByEmail(ctx, pool, "bruno@example.test")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("error = %v, want store.ErrNotFound", err)
	}
}
