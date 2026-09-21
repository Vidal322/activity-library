package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Vidal322/activity-library/internal/store"
	"github.com/Vidal322/activity-library/internal/testutil"
)

// PUT /v1/games/{id}/blocks replaces the list rather than patching it, so the
// three things every test below is really watching are: the ids of the blocks
// that stayed, the order they come back in, and the positions, which the
// server numbers 0..n-1 whatever the client sent.

// blockGame is the row the block tests write to: authored by the caller, so
// nothing here turns on authorship except the test that means to.
const blockGame = testGamePublishedOlder

// Five ids for the reorder, out of id order on purpose: the reversal has to be
// visible in the list, not in how the rows happen to sort by id.
const (
	testBlockOne   = "70000000-0000-7000-8000-0000000000b3"
	testBlockTwo   = "70000000-0000-7000-8000-0000000000b1"
	testBlockThree = "70000000-0000-7000-8000-0000000000b5"
	testBlockFour  = "70000000-0000-7000-8000-0000000000b2"
	testBlockFive  = "70000000-0000-7000-8000-0000000000b4"

	// Well formed, deliberately never inserted.
	testBlockMissing = "70000000-0000-7000-8000-0000000000bf"
)

// fiveBlocks is the seeded list, in position order.
func fiveBlocks() []string {
	return []string{testBlockOne, testBlockTwo, testBlockThree, testBlockFour, testBlockFive}
}

// seedBlocksFixture writes the game and five blocks at positions 0..4.
func seedBlocksFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	insertGame(t, ctx, pool, testGame{
		ID:              blockGame,
		Title:           "Jogo do Lenço",
		Description:     "Two teams, one handkerchief",
		MinParticipants: ptr(int32(6)),
		MaxParticipants: ptr(int32(30)),
		DurationMin:     ptr(int32(15)),
		DurationMax:     ptr(int32(40)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Authors:         []string{testSessionUserID},
	})

	for i, id := range fiveBlocks() {
		insertBlock(t, ctx, pool, blockGame, gameBlock{
			ID:       id,
			Type:     "paragraph",
			Content:  blockContent(i),
			Position: int32(i),
		})
	}
}

// blockContent gives each seeded block a body of its own, so a test can tell
// which block it is looking at without trusting the id it came with.
func blockContent(i int) string {
	return "Step " + string(rune('A'+i))
}

// putBlocks returns the response untouched, for the tests asserting on a
// refusal.
func putBlocks(t *testing.T, baseURL, id, body string) (int, []byte) {
	t.Helper()

	res, err := authedPut(t, baseURL+"/v1/games/"+id+"/blocks", body)
	if err != nil {
		t.Fatalf("PUT /v1/games/%s/blocks: %v", id, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("could not read the response body: %v", err)
	}

	return res.StatusCode, raw
}

// editBlocks replaces the list expecting success and decodes the game.
func editBlocks(t *testing.T, baseURL, id, body string) gameDetail {
	t.Helper()

	status, raw := putBlocks(t, baseURL, id, body)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, raw)
	}

	var got gameDetail
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	return got
}

// assertPositions holds the response to the promise the endpoint makes about
// numbering: 0..n-1, in the order the blocks appear.
func assertPositions(t *testing.T, blocks []gameBlock) {
	t.Helper()

	for i, b := range blocks {
		if b.Position != int32(i) {
			t.Errorf("blocks[%d].position = %d, want %d", i, b.Position, i)
		}
	}
}

func blockIDs(blocks []gameBlock) []string {
	ids := make([]string, len(blocks))
	for i, b := range blocks {
		ids[i] = b.ID
	}

	return ids
}

func assertBlockIDs(t *testing.T, got []gameBlock, want ...string) {
	t.Helper()

	ids := blockIDs(got)

	if len(ids) != len(want) {
		t.Fatalf("got %d blocks %v, want %d %v", len(ids), ids, len(want), want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("blocks[%d].id = %s, want %s (full order %v)", i, ids[i], want[i], ids)
		}
	}
}

// readBlocks reads the list straight out of the table, so a test can check
// what was stored rather than what the response said was stored.
func readBlocks(t *testing.T, ctx context.Context, pool *pgxpool.Pool, gameID string) []gameBlock {
	t.Helper()

	rows, err := pool.Query(ctx, `
		SELECT id, type, content, position FROM blocks
		WHERE game_id = $1 ORDER BY position`, gameID)
	if err != nil {
		t.Fatalf("could not read the blocks back: %v", err)
	}
	defer rows.Close()

	var blocks []gameBlock
	for rows.Next() {
		var b gameBlock
		if err := rows.Scan(&b.ID, &b.Type, &b.Content, &b.Position); err != nil {
			t.Fatalf("could not scan a block: %v", err)
		}
		blocks = append(blocks, b)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("could not read the blocks back: %v", err)
	}

	return blocks
}

// TestHandleEditGameBlocksReversesTheList is the case the endpoint exists for,
// and the one that needs blocks_game_position_key to be deferred: five blocks
// come back in the opposite order, under the same five ids, numbered from zero
// again. Every intermediate update in that transaction collides with a
// position another row still holds.
func TestHandleEditGameBlocksReversesTheList(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedBlocksFixture(t, ctx, pool)

	got := editBlocks(t, srv.URL, blockGame, `{"blocks": [
		{"id": "`+testBlockFive+`", "type": "paragraph", "content": "Step E"},
		{"id": "`+testBlockFour+`", "type": "paragraph", "content": "Step D"},
		{"id": "`+testBlockThree+`", "type": "paragraph", "content": "Step C"},
		{"id": "`+testBlockTwo+`", "type": "paragraph", "content": "Step B"},
		{"id": "`+testBlockOne+`", "type": "paragraph", "content": "Step A"}
	]}`)

	assertBlockIDs(t, got.Blocks,
		testBlockFive, testBlockFour, testBlockThree, testBlockTwo, testBlockOne)
	assertPositions(t, got.Blocks)

	// And the same again from the table, since the response is assembled by a
	// fresh read and could agree with itself while the rows disagreed.
	stored := readBlocks(t, ctx, pool, blockGame)
	assertBlockIDs(t, stored,
		testBlockFive, testBlockFour, testBlockThree, testBlockTwo, testBlockOne)
	assertPositions(t, stored)

	// The content travelled with the ids rather than staying at the positions.
	if stored[0].Content != "Step E" {
		t.Errorf("blocks[0].content = %q, want %q", stored[0].Content, "Step E")
	}
}

// TestHandleEditGameBlocksNumbersFromZeroWhateverTheClientSays is the other
// half of that promise: the client's own numbering never reaches the table,
// because the request has nowhere to put it.
func TestHandleEditGameBlocksNumbersFromZeroWhateverTheClientSays(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedBlocksFixture(t, ctx, pool)

	status, raw := putBlocks(t, srv.URL, blockGame, `{"blocks": [
		{"id": "`+testBlockOne+`", "type": "paragraph", "content": "Step A", "position": 9}
	]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusBadRequest, raw)
	}
	if !strings.Contains(string(raw), "position") {
		t.Errorf("body = %s, want it to name the field it refused", raw)
	}

	// Refused before anything was written.
	if stored := readBlocks(t, ctx, pool, blockGame); len(stored) != 5 {
		t.Errorf("got %d blocks, want the fixture's 5 untouched", len(stored))
	}
}

// TestHandleEditGameBlocksInsertsAddsAndDropsOmissions covers the three things
// one request can do at once: a block with an id is kept, a block without one
// is created, and a block the list no longer names is gone.
func TestHandleEditGameBlocksInsertsAddsAndDropsOmissions(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedBlocksFixture(t, ctx, pool)

	got := editBlocks(t, srv.URL, blockGame, `{"blocks": [
		{"type": "heading", "content": "Setup"},
		{"id": "`+testBlockTwo+`", "type": "paragraph", "content": "Step B, reworded"}
	]}`)

	if len(got.Blocks) != 2 {
		t.Fatalf("got %d blocks %v, want 2", len(got.Blocks), blockIDs(got.Blocks))
	}
	assertPositions(t, got.Blocks)

	if got.Blocks[0].Type != "heading" || got.Blocks[0].Content != "Setup" {
		t.Errorf("blocks[0] = %+v, want the new heading", got.Blocks[0])
	}
	if got.Blocks[0].ID == "" {
		t.Errorf("blocks[0].id is empty, want an id assigned by the database")
	}

	// The kept block kept its id, which is the whole reason ids are accepted
	// on the way in.
	if got.Blocks[1].ID != testBlockTwo {
		t.Errorf("blocks[1].id = %s, want %s", got.Blocks[1].ID, testBlockTwo)
	}
	if got.Blocks[1].Content != "Step B, reworded" {
		t.Errorf("blocks[1].content = %q, want the new text", got.Blocks[1].Content)
	}

	if stored := readBlocks(t, ctx, pool, blockGame); len(stored) != 2 {
		t.Errorf("got %d stored blocks %v, want 2", len(stored), blockIDs(stored))
	}
}

// TestHandleEditGameBlocksAcceptsAnEmptyList is a replace with nothing in it,
// which is how an editor clears a game. It has to be distinguishable from a
// body that forgot the field, which the test below covers.
func TestHandleEditGameBlocksAcceptsAnEmptyList(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedBlocksFixture(t, ctx, pool)

	got := editBlocks(t, srv.URL, blockGame, `{"blocks": []}`)

	if len(got.Blocks) != 0 {
		t.Errorf("blocks = %v, want an empty list", blockIDs(got.Blocks))
	}
	if got.Blocks == nil {
		t.Errorf("blocks = null, want an empty array")
	}
	if stored := readBlocks(t, ctx, pool, blockGame); len(stored) != 0 {
		t.Errorf("got %d stored blocks, want none", len(stored))
	}
}

// TestHandleEditGameBlocksRefusesABodyWithoutTheField separates "replace with
// nothing" from "did not say", which an implementation reading the field into
// a plain slice would collapse into the first and silently empty the game.
func TestHandleEditGameBlocksRefusesABodyWithoutTheField(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"absent", `{}`},
		{"null", `{"blocks": null}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, pool := newAuthedTestServer(t)
			ctx := testContext(t)

			seedBlocksFixture(t, ctx, pool)

			status, raw := putBlocks(t, srv.URL, blockGame, tc.body)
			if status != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want %d (body %s)",
					status, http.StatusUnprocessableEntity, raw)
			}

			if stored := readBlocks(t, ctx, pool, blockGame); len(stored) != 5 {
				t.Errorf("got %d blocks, want the fixture's 5 untouched", len(stored))
			}
		})
	}
}

// TestHandleEditGameBlocksRefusesAnUnknownBlock covers the id that names no
// block of this game — including one that belongs to somebody else's game,
// which must not be moved across rather than reported.
func TestHandleEditGameBlocksRefusesAnUnknownBlock(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedBlocksFixture(t, ctx, pool)
	seedSomeoneElsesGame(t, ctx, pool)
	insertBlock(t, ctx, pool, testGamePublishedNewer, gameBlock{
		ID:       testBlockHeading,
		Type:     "heading",
		Content:  "Not yours",
		Position: 0,
	})

	cases := []struct {
		name string
		id   string
	}{
		{"never inserted", testBlockMissing},
		{"another game's", testBlockHeading},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, raw := putBlocks(t, srv.URL, blockGame, `{"blocks": [
				{"id": "`+tc.id+`", "type": "paragraph", "content": "Mine now"}
			]}`)
			if status != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want %d (body %s)",
					status, http.StatusUnprocessableEntity, raw)
			}
			if !strings.Contains(string(raw), tc.id) {
				t.Errorf("body = %s, want it to name the block %s", raw, tc.id)
			}

			// The transaction rolled back whole: the four blocks this request
			// would have deleted are all still there.
			if stored := readBlocks(t, ctx, pool, blockGame); len(stored) != 5 {
				t.Errorf("got %d blocks %v, want the fixture's 5 untouched",
					len(stored), blockIDs(stored))
			}
		})
	}

	// And the other game's block stayed where it was.
	other := readBlocks(t, ctx, pool, testGamePublishedNewer)
	if len(other) != 1 || other[0].Content != "Not yours" {
		t.Errorf("the other game's blocks = %+v, want them untouched", other)
	}
}

// TestHandleEditGameBlocksRefusesARepeatedID guards the numbering: the same
// block listed twice would be updated twice, the second position winning and
// the first left to nobody, so the list would come back short of contiguous.
func TestHandleEditGameBlocksRefusesARepeatedID(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedBlocksFixture(t, ctx, pool)

	status, raw := putBlocks(t, srv.URL, blockGame, `{"blocks": [
		{"id": "`+testBlockOne+`", "type": "paragraph", "content": "Step A"},
		{"id": "`+testBlockTwo+`", "type": "paragraph", "content": "Step B"},
		{"id": "`+testBlockOne+`", "type": "paragraph", "content": "Step A again"}
	]}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusUnprocessableEntity, raw)
	}

	if stored := readBlocks(t, ctx, pool, blockGame); len(stored) != 5 {
		t.Errorf("got %d blocks, want the fixture's 5 untouched", len(stored))
	}
}

// TestHandleEditGameBlocksRefusesAMalformedID keeps a value that is not a uuid
// out of the query, where it would surface as a failure of the server rather
// than of the request.
func TestHandleEditGameBlocksRefusesAMalformedID(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedBlocksFixture(t, ctx, pool)

	status, raw := putBlocks(t, srv.URL, blockGame, `{"blocks": [
		{"id": "not-a-uuid", "type": "paragraph", "content": "Step A"}
	]}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusUnprocessableEntity, raw)
	}
}

// TestBlockTypesMatchTheConstraint is the seam between blockTypes and
// blocks_type_check. Nothing but this test holds the two together: a type added
// to the migration and not to the slice is refused by the API that should allow
// it, and one added to the slice and not to the migration turns the 422 below
// back into the 500 the validation exists to remove.
//
// It reads the constraint back from the catalog rather than the .sql file, so
// what it compares against is the rule the database is actually enforcing.
func TestBlockTypesMatchTheConstraint(t *testing.T) {
	pool := testutil.RequirePool(t)
	ctx := testContext(t)

	var def string
	err := pool.QueryRow(ctx,
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint
		 WHERE conname = 'blocks_type_check'`,
	).Scan(&def)
	if err != nil {
		t.Fatalf("could not read blocks_type_check: %v", err)
	}

	// pg_get_constraintdef prints the allowed values as quoted literals:
	// CHECK ((type = ANY (ARRAY['paragraph'::text, 'steps'::text, ...]))).
	// Every literal in there is a type, and there is nothing else quoted.
	var inConstraint []string
	for _, m := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(def, -1) {
		inConstraint = append(inConstraint, m[1])
	}

	slices.Sort(inConstraint)
	inGo := slices.Clone(blockTypes)
	slices.Sort(inGo)

	if !slices.Equal(inGo, inConstraint) {
		t.Errorf("blockTypes = %v, want the constraint's %v (from %s)",
			inGo, inConstraint, def)
	}
}

// TestHandleEditGameBlocksRefusesAnUnknownType covers the type the schema has
// never allowed. The handler refuses it by name before the transaction opens,
// so the body names the types that would have worked rather than reporting a
// constraint the client cannot read.
func TestHandleEditGameBlocksRefusesAnUnknownType(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedBlocksFixture(t, ctx, pool)

	status, raw := putBlocks(t, srv.URL, blockGame,
		`{"blocks": [{"type": "carousel", "content": "Step A"}]}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusUnprocessableEntity, raw)
	}
	// The point of validating ahead of the constraint is the list: a client
	// that sent the wrong type learns which ones are right.
	for _, want := range blockTypes {
		if !strings.Contains(string(raw), want) {
			t.Errorf("body = %s, want it to name the valid type %s", raw, want)
		}
	}

	if !strings.Contains(string(raw), "blocks[0].type") {
		t.Errorf("body = %s, want it to point at the block that was wrong", raw)
	}

	if stored := readBlocks(t, ctx, pool, blockGame); len(stored) != 5 {
		t.Errorf("got %d blocks, want the fixture's 5 untouched", len(stored))
	}
}

// TestHandleEditGameBlocksRejectsAMalformedGameID stops at the path, before
// the authorship query has a chance to fail on the value instead.
func TestHandleEditGameBlocksRejectsAMalformedGameID(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	status, raw := putBlocks(t, srv.URL, "not-a-uuid", `{"blocks": []}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusBadRequest, raw)
	}
}

func TestHandleEditGameBlocksRejectsAMissingGame(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	status, raw := putBlocks(t, srv.URL, testGameMissing, `{"blocks": []}`)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusNotFound, raw)
	}
}

func TestHandleEditGameBlocksNeedsASession(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedBlocksFixture(t, ctx, pool)

	res, err := http.Post(srv.URL+"/v1/games/"+blockGame+"/blocks", "application/json",
		strings.NewReader(`{"blocks": []}`))
	if err != nil {
		t.Fatalf("POST without a session: %v", err)
	}
	defer res.Body.Close()

	// The route takes PUT, so an unauthenticated POST is refused by the router
	// rather than by the middleware; what matters is that it is refused.
	if res.StatusCode == http.StatusOK {
		t.Fatalf("status = %d, want a refusal", res.StatusCode)
	}

	req, err := http.NewRequest(http.MethodPut,
		srv.URL+"/v1/games/"+blockGame+"/blocks", strings.NewReader(`{"blocks": []}`))
	if err != nil {
		t.Fatalf("could not build the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT without a session: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusUnauthorized)
	}

	if stored := readBlocks(t, ctx, pool, blockGame); len(stored) != 5 {
		t.Errorf("got %d blocks, want the fixture's 5 untouched", len(stored))
	}
}

func TestHandleEditGameBlocksRejectsANonAuthor(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedSomeoneElsesGame(t, ctx, pool)
	insertBlock(t, ctx, pool, testGamePublishedNewer, gameBlock{
		ID:       testBlockHeading,
		Type:     "heading",
		Content:  "Not yours",
		Position: 0,
	})

	status, raw := putBlocks(t, srv.URL, testGamePublishedNewer, `{"blocks": []}`)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusForbidden, raw)
	}

	if stored := readBlocks(t, ctx, pool, testGamePublishedNewer); len(stored) != 1 {
		t.Errorf("got %d blocks, want the one the other author wrote", len(stored))
	}
}

// TestEditGameBlocksStatementRefusesANonAuthor reaches past the handler on
// purpose, the same way the edit tests do: the handler's own check would let
// every test above pass against a transaction that happily rewrote anyone's
// game.
func TestEditGameBlocksStatementRefusesANonAuthor(t *testing.T) {
	_, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedSomeoneElsesGame(t, ctx, pool)
	insertBlock(t, ctx, pool, testGamePublishedNewer, gameBlock{
		ID:       testBlockHeading,
		Type:     "heading",
		Content:  "Not yours",
		Position: 0,
	})

	_, err := store.EditGameBlocks(ctx, pool, testGamePublishedNewer,
		[]store.GameBlockInput{}, testSessionUserID)
	if !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("EditGameBlocks() error = %v, want %v", err, store.ErrForbidden)
	}

	if stored := readBlocks(t, ctx, pool, testGamePublishedNewer); len(stored) != 1 {
		t.Errorf("got %d blocks, want the one the other author wrote", len(stored))
	}
}
