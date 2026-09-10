package api

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Vidal322/activity-library/internal/testutil"
)

// These tests cover the game_authors join table and the rule that a game must
// always name at least one author. The rule lives in the schema, as a pair of
// deferred constraint triggers, so the assertions here go to the database
// directly rather than through a handler: there is no endpoint that writes a
// game yet, and the invariant has to hold whatever eventually calls it.

// Two authors whose names sort the opposite way round from the order they are
// inserted in, and from their ids. An ORDER BY that was dropped, or one that
// sorted by insertion, would still look right if these lined up.
const (
	testAuthorAlvaro = "10000000-0000-7000-8000-0000000000b2"
	testAuthorZe     = "10000000-0000-7000-8000-0000000000b1"

	nameAlvaro = "Álvaro Nunes"
	nameZe     = "Zé Pinto"
)

// authorsDB truncates and returns the pool, for the tests that never speak HTTP.
func authorsDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	pool := testutil.RequirePool(t)
	testutil.Truncate(t, pool)

	return pool
}

// constraintName reports the constraint Postgres blamed, so a test can assert
// on the rule that fired rather than on the wording of a message.
func constraintName(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}

	return ""
}

// insertBareGame writes a game and nothing else, in its own transaction, and
// returns whatever the commit made of it. Everything in this file that expects
// the author rule to fire goes through it.
func insertBareGame(ctx context.Context, pool *pgxpool.Pool, id, title string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO games (
			id, title, min_participants, max_participants,
			duration_min, duration_max)
		VALUES ($1, $2, 2, 10, 5, 15)`,
		id, title)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func TestGameCanHaveTwoAuthors(t *testing.T) {
	ctx := testContext(t)
	pool := authorsDB(t)

	insertNamedAuthor(t, ctx, pool, testAuthorZe, nameZe)
	insertNamedAuthor(t, ctx, pool, testAuthorAlvaro, nameAlvaro)

	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "Co-written",
		MinParticipants: ptr(int32(2)),
		MaxParticipants: ptr(int32(10)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(15)),
		PublishState:    "published",
		Authors:         []string{testAuthorZe, testAuthorAlvaro},
	})

	rows, err := pool.Query(ctx, `
		SELECT u.name
		FROM game_authors ga
		JOIN users u ON u.id = ga.user_id
		WHERE ga.game_id = $1
		ORDER BY u.name COLLATE "pt-PT-x-icu", u.id`,
		testGamePublishedNewer)
	if err != nil {
		t.Fatalf("could not read the authors back: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("could not scan an author: %v", err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("could not read the authors back: %v", err)
	}

	// Alphabetical, so the accented name comes first. Under the database's
	// default collation it would sort after Zé, which is the whole reason the
	// query names a collation.
	want := []string{nameAlvaro, nameZe}
	if len(got) != len(want) {
		t.Fatalf("got %d authors (%v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("author %d is %q, want %q (full order %v)", i, got[i], want[i], got)
		}
	}
}

func TestGameWithoutAuthorsIsRejected(t *testing.T) {
	ctx := testContext(t)
	pool := authorsDB(t)

	err := insertBareGame(ctx, pool, testGamePublishedNewer, "Unattributed")
	if err == nil {
		t.Fatal("a game with no authors was accepted, want the commit to fail")
	}

	if got := constraintName(err); got != "games_author_required" {
		t.Errorf("blamed constraint is %q, want games_author_required (error: %v)", got, err)
	}
}

func TestLastAuthorCannotBeRemoved(t *testing.T) {
	ctx := testContext(t)
	pool := authorsDB(t)

	insertNamedAuthor(t, ctx, pool, testAuthorZe, nameZe)
	insertNamedAuthor(t, ctx, pool, testAuthorAlvaro, nameAlvaro)

	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "Co-written",
		MinParticipants: ptr(int32(2)),
		MaxParticipants: ptr(int32(10)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(15)),
		PublishState:    "published",
		Authors:         []string{testAuthorZe, testAuthorAlvaro},
	})

	// Dropping one of two is ordinary editing and must be allowed.
	_, err := pool.Exec(ctx,
		`DELETE FROM game_authors WHERE game_id = $1 AND user_id = $2`,
		testGamePublishedNewer, testAuthorZe)
	if err != nil {
		t.Fatalf("could not drop one author of two: %v", err)
	}

	// Dropping the survivor would leave the game unattributed.
	_, err = pool.Exec(ctx,
		`DELETE FROM game_authors WHERE game_id = $1 AND user_id = $2`,
		testGamePublishedNewer, testAuthorAlvaro)
	if err == nil {
		t.Fatal("the last author was removed, want the delete to fail")
	}

	if got := constraintName(err); got != "games_author_required" {
		t.Errorf("blamed constraint is %q, want games_author_required (error: %v)", got, err)
	}
}

// The rule has to survive an author row being moved as well as deleted: an
// UPDATE that changes game_id empties the game the row left, and no DELETE
// fires on the way out.
func TestAuthorCannotBeMovedOffItsOnlyGame(t *testing.T) {
	ctx := testContext(t)
	pool := authorsDB(t)

	insertNamedAuthor(t, ctx, pool, testAuthorZe, nameZe)
	insertNamedAuthor(t, ctx, pool, testAuthorAlvaro, nameAlvaro)

	for _, g := range []struct {
		id     string
		title  string
		author string
	}{
		{testGamePublishedNewer, "First", testAuthorZe},
		{testGamePublishedOlder, "Second", testAuthorAlvaro},
	} {
		insertGame(t, ctx, pool, testGame{
			ID:              g.id,
			Title:           g.title,
			MinParticipants: ptr(int32(2)),
			MaxParticipants: ptr(int32(10)),
			DurationMin:     ptr(int32(5)),
			DurationMax:     ptr(int32(15)),
			PublishState:    "published",
			Authors:         []string{g.author},
		})
	}

	// The destination has a different author, so this does not collide with
	// the primary key: nothing but the trigger stands in its way.
	_, err := pool.Exec(ctx,
		`UPDATE game_authors SET game_id = $1 WHERE game_id = $2`,
		testGamePublishedOlder, testGamePublishedNewer)
	if err == nil {
		t.Fatal("an author was moved off its only game, want the update to fail")
	}

	if got := constraintName(err); got != "games_author_required" {
		t.Errorf("blamed constraint is %q, want games_author_required (error: %v)", got, err)
	}
}

func TestDeletingAnAuthorWithGamesFails(t *testing.T) {
	ctx := testContext(t)
	pool := authorsDB(t)

	insertAuthor(t, ctx, pool, testAuthorID)
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "Attributed",
		MinParticipants: ptr(int32(2)),
		MaxParticipants: ptr(int32(10)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(15)),
		PublishState:    "published",
	})

	_, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, testAuthorID)
	if err == nil {
		t.Fatal("a user who authored a game was deleted, want the delete to fail")
	}

	// The name the API maps to "that user still has games".
	if got := constraintName(err); got != "game_authors_user_id_fkey" {
		t.Errorf("blamed constraint is %q, want game_authors_user_id_fkey (error: %v)", got, err)
	}
}

// Deleting the game is the one case that empties game_authors legitimately:
// the cascade takes the rows and the game with them, so the rule has nothing
// left to protect.
func TestDeletingAGameRemovesItsAuthorRows(t *testing.T) {
	ctx := testContext(t)
	pool := authorsDB(t)

	insertAuthor(t, ctx, pool, testAuthorID)
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "Doomed",
		MinParticipants: ptr(int32(2)),
		MaxParticipants: ptr(int32(10)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(15)),
		PublishState:    "published",
	})

	if _, err := pool.Exec(ctx, `DELETE FROM games WHERE id = $1`, testGamePublishedNewer); err != nil {
		t.Fatalf("could not delete the game: %v", err)
	}

	var left int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM game_authors WHERE game_id = $1`,
		testGamePublishedNewer).Scan(&left); err != nil {
		t.Fatalf("could not count the author rows: %v", err)
	}

	if left != 0 {
		t.Errorf("%d author rows outlived the game, want 0", left)
	}
}

// The direction the single column could not serve: everything one user wrote.
func TestGamesCanBeListedByAuthor(t *testing.T) {
	ctx := testContext(t)
	pool := authorsDB(t)

	insertNamedAuthor(t, ctx, pool, testAuthorZe, nameZe)
	insertNamedAuthor(t, ctx, pool, testAuthorAlvaro, nameAlvaro)

	// Zé wrote the first two, Álvaro co-wrote only the second.
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "Solo",
		MinParticipants: ptr(int32(2)),
		MaxParticipants: ptr(int32(10)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(15)),
		PublishState:    "published",
		Authors:         []string{testAuthorZe},
	})
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedOlder,
		Title:           "Shared",
		MinParticipants: ptr(int32(2)),
		MaxParticipants: ptr(int32(10)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(15)),
		PublishState:    "published",
		Authors:         []string{testAuthorZe, testAuthorAlvaro},
	})

	for _, tc := range []struct {
		name   string
		author string
		want   int
	}{
		{"an author of both", testAuthorZe, 2},
		{"an author of one", testAuthorAlvaro, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got int
			if err := pool.QueryRow(ctx,
				`SELECT count(*) FROM game_authors WHERE user_id = $1`,
				tc.author).Scan(&got); err != nil {
				t.Fatalf("could not count the games: %v", err)
			}

			if got != tc.want {
				t.Errorf("author has %d games, want %d", got, tc.want)
			}
		})
	}
}
