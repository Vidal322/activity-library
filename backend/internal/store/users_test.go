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

// The tests below cover store.CreateUser, the write behind registration.

// TestCreateUserReturnsTheStoredRow pins what the caller gets back. The INSERT
// returns the row rather than just an id so the handler can answer without a
// second query, which only works if RETURNING keeps naming every column the
// struct has.
func TestCreateUserReturnsTheStoredRow(t *testing.T) {
	pool, ctx := usersTest(t)

	img := "https://example.test/ana.png"

	user, err := store.CreateUser(ctx, pool, "Ana Marques", "ana@example.test", "not-a-real-hash", &img)
	if err != nil {
		t.Fatalf("could not create an account: %v", err)
	}

	if user.ID == "" {
		t.Error("ID is empty, want the uuid the column defaulted to")
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
	if user.CreatedAt.IsZero() {
		t.Error("CreatedAt is the zero time, want the value the column defaulted to")
	}

	// The row the caller was handed has to be the row that landed, not a
	// struct assembled from the arguments.
	stored, err := store.GetUserByID(ctx, pool, user.ID)
	if err != nil {
		t.Fatalf("could not read the new account back: %v", err)
	}
	if stored.Email != user.Email {
		t.Errorf("stored Email = %q, want %q", stored.Email, user.Email)
	}
}

// TestCreateUserTakesTheColumnDefaults is a privilege check standing in a test
// file. createUserQuery names neither role nor active, so a registration cannot
// choose its own standing however the request is shaped: the account arrives as
// an outsider and the schema decides, not the caller.
func TestCreateUserTakesTheColumnDefaults(t *testing.T) {
	pool, ctx := usersTest(t)

	user, err := store.CreateUser(ctx, pool, "Ana Marques", "ana@example.test", "not-a-real-hash", nil)
	if err != nil {
		t.Fatalf("could not create an account: %v", err)
	}

	if user.Role != "outsider" {
		t.Errorf("Role = %q, want %q", user.Role, "outsider")
	}
	if !user.Active {
		t.Error("Active = false, want true")
	}
}

// TestCreateUserAcceptsNoImg covers the nullable column on the way in: most
// registrations carry no picture.
func TestCreateUserAcceptsNoImg(t *testing.T) {
	pool, ctx := usersTest(t)

	user, err := store.CreateUser(ctx, pool, "Bruno Costa", "bruno@example.test", "not-a-real-hash", nil)
	if err != nil {
		t.Fatalf("could not create an account without a picture: %v", err)
	}

	if user.Img != nil {
		t.Errorf("Img = %q, want nil", *user.Img)
	}
}

// TestCreateUserRejectsADuplicateEmail is the ErrConflict half of issue #25.
// The sentinel is what the handler keys on for its 409, and the constraint name
// rides along inside ConstraintError so writeStoreError can name the field that
// collided rather than saying something already exists.
func TestCreateUserRejectsADuplicateEmail(t *testing.T) {
	pool, ctx := usersTest(t)

	if _, err := store.CreateUser(ctx, pool, "Ana Marques", "ana@example.test", "not-a-real-hash", nil); err != nil {
		t.Fatalf("could not create the first account: %v", err)
	}

	_, err := store.CreateUser(ctx, pool, "Ana Again", "ana@example.test", "not-a-real-hash", nil)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("error = %v, want store.ErrConflict", err)
	}

	var ce *store.ConstraintError
	if !errors.As(err, &ce) {
		t.Fatalf("error is not a ConstraintError: %v", err)
	}
	if ce.Constraint != "users_email_key" {
		t.Errorf("Constraint = %q, want %q", ce.Constraint, "users_email_key")
	}
}

// TestCreateUserFoldsCaseOnTheDuplicate is the same conflict arriving in a
// different case. users_email_key is on lower(email), so this is the database's
// guarantee rather than the query's, and it has to hold for the store too: two
// people cannot hold one address by capitalising it differently.
func TestCreateUserFoldsCaseOnTheDuplicate(t *testing.T) {
	pool, ctx := usersTest(t)

	if _, err := store.CreateUser(ctx, pool, "Ana Marques", "ana@example.test", "not-a-real-hash", nil); err != nil {
		t.Fatalf("could not create the first account: %v", err)
	}

	_, err := store.CreateUser(ctx, pool, "Ana Again", "ANA@Example.TEST", "not-a-real-hash", nil)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("error = %v, want store.ErrConflict", err)
	}
}

// TestCreateUserReusesADeletedEmail is the partial index seen from the write
// side, and the reason the conflict above is not simply unique. Deletion is
// final and coming back means a new account, so a deleted account releases its
// address: without WHERE active on the index this registration would collide
// with a row nobody can log into.
func TestCreateUserReusesADeletedEmail(t *testing.T) {
	pool, ctx := usersTest(t)

	first, err := store.CreateUser(ctx, pool, "Ana Marques", "ana@example.test", "not-a-real-hash", nil)
	if err != nil {
		t.Fatalf("could not create the first account: %v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE users SET active = false WHERE id = $1`, first.ID); err != nil {
		t.Fatalf("could not delete the first account: %v", err)
	}

	second, err := store.CreateUser(ctx, pool, "Ana Marques", "ana@example.test", "not-a-real-hash", nil)
	if err != nil {
		t.Fatalf("a deleted account still holds its address, want it released: %v", err)
	}

	if second.ID == first.ID {
		t.Error("the new account reused the deleted account's id, want a new row")
	}
}

// TestCreateUserRejectsEmptyFields pins the check constraints reaching the
// caller as ErrInvalid. The handler validates before it ever gets here, so this
// is the guarantee for anything that does not: a future seeder, an import, a
// second handler written in a hurry.
func TestCreateUserRejectsEmptyFields(t *testing.T) {
	pool, ctx := usersTest(t)

	for _, tc := range []struct {
		label, name, email string
	}{
		{"empty name", "", "ana@example.test"},
		{"empty email", "Ana Marques", ""},
	} {
		t.Run(tc.label, func(t *testing.T) {
			_, err := store.CreateUser(ctx, pool, tc.name, tc.email, "not-a-real-hash", nil)
			if !errors.Is(err, store.ErrInvalid) {
				t.Fatalf("error = %v, want store.ErrInvalid", err)
			}
		})
	}
}
