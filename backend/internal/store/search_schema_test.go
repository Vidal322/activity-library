package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests are the search triggers' own guarantee, not the store's:
// game_search.document has to track every block column refresh_game_search
// reads, and position is one of them. They share TestMain, queryTimeout and
// usersTest with users_schema_test.go.

// searchGameID and the ids below are fixed rather than generated so a failure
// names the row it is about.
const (
	searchGameID   = "20000000-0000-7000-8000-000000000001"
	searchAuthorID = "20000000-0000-7000-8000-000000000002"
)

const (
	searchBlockFirst  = "20000000-0000-7000-8000-000000000010"
	searchBlockSecond = "20000000-0000-7000-8000-000000000011"
)

// seedReorderFixture writes one game and the two prose blocks whose order is
// the subject of the test. The two contents share no word, so the aggregated
// document's lexeme set is the same whichever order they are in and the only
// thing a reorder can change is where each lexeme sits.
//
// It all goes in one transaction because games_require_author is a deferred
// constraint trigger: a game committed without a row in game_authors is
// refused, and the author cannot be inserted before the game it references.
func seedReorderFixture(t *testing.T, pool *pgxpool.Pool, ctx context.Context) {
	t.Helper()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("could not open the fixture transaction: %v", err)
	}
	defer tx.Rollback(ctx)

	exec := func(what, sql string, args ...any) {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("could not insert %s: %v", what, err)
		}
	}

	exec("the author account", `
		INSERT INTO users (id, name, email, pass_hash)
		VALUES ($1, 'Search Author', 'search@example.test', 'not-a-real-hash')`,
		searchAuthorID)

	exec("the game", `
		INSERT INTO games (id, title, min_participants, max_participants,
		                   duration_min, duration_max)
		VALUES ($1, 'Corrida de Sacos', 4, 12, 10, 20)`,
		searchGameID)

	exec("the authorship", `
		INSERT INTO game_authors (game_id, user_id) VALUES ($1, $2)`,
		searchGameID, searchAuthorID)

	blocks := []struct {
		id       string
		content  string
		position int
	}{
		{searchBlockFirst, "Abacate maduro em cima da mesa.", 0},
		{searchBlockSecond, "Bicicleta velha encostada ao muro.", 1},
	}

	for _, b := range blocks {
		exec("block "+b.id, `
			INSERT INTO blocks (id, game_id, type, content, position)
			VALUES ($1, $2, 'paragraph', $3, $4)`,
			b.id, searchGameID, b.content, b.position)
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("could not commit the fixture: %v", err)
	}
}

// readSearchDocument returns the stored document in its text form, which spells
// out every lexeme with its positions and weights. Comparing that text is how
// the tests below see a position change: 'abacat':1C and 'abacat':5C are the
// same lexeme and different documents.
func readSearchDocument(t *testing.T, pool *pgxpool.Pool, ctx context.Context) string {
	t.Helper()

	var document string
	if err := pool.QueryRow(ctx,
		`SELECT document::text FROM game_search WHERE game_id = $1`, searchGameID,
	).Scan(&document); err != nil {
		t.Fatalf("could not read the stored document: %v", err)
	}

	return document
}

// TestBlockReorderRefreshesSearchDocument is issue 42. refresh_game_search
// aggregates block content in position order, so position decides the lexeme
// positions ts_rank_cd reads for its proximity term, and blocks_refresh_search
// did not watch that column: a reordered game went on ranking against the word
// order it used to have.
//
// The statement below assigns position and nothing else, which is what a
// granular reorder endpoint would issue. EditGameBlocks names content in every
// SET and so fires the trigger whatever it is watching, which is exactly why
// this bug could sit in the schema unnoticed: a test driven through that path
// would pass either way.
func TestBlockReorderRefreshesSearchDocument(t *testing.T) {
	pool, ctx := usersTest(t)
	seedReorderFixture(t, pool, ctx)

	before := readSearchDocument(t, pool, ctx)

	// One statement, so the swap passes through the duplicate position the
	// deferred blocks_game_position_key exists to allow.
	if _, err := pool.Exec(ctx,
		`UPDATE blocks SET position = 1 - position WHERE game_id = $1`, searchGameID,
	); err != nil {
		t.Fatalf("could not reorder the blocks: %v", err)
	}

	after := readSearchDocument(t, pool, ctx)

	if after == before {
		t.Fatalf("the stored document survived a reorder unchanged: %s", after)
	}
}

// TestBlockReorderRefreshesSearchDocumentCompletely is the other half: a
// trigger that fired but aggregated the wrong thing would satisfy the test
// above. Recomputing the document by hand and finding it already stored is what
// says the trigger left nothing for a later edit to repair.
func TestBlockReorderRefreshesSearchDocumentCompletely(t *testing.T) {
	pool, ctx := usersTest(t)
	seedReorderFixture(t, pool, ctx)

	if _, err := pool.Exec(ctx,
		`UPDATE blocks SET position = 1 - position WHERE game_id = $1`, searchGameID,
	); err != nil {
		t.Fatalf("could not reorder the blocks: %v", err)
	}

	stored := readSearchDocument(t, pool, ctx)

	if _, err := pool.Exec(ctx,
		`SELECT refresh_game_search($1)`, searchGameID,
	); err != nil {
		t.Fatalf("could not refresh the document by hand: %v", err)
	}

	if want := readSearchDocument(t, pool, ctx); stored != want {
		t.Errorf("stored document = %s, want %s", stored, want)
	}
}
