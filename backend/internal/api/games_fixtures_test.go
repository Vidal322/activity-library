package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Fixed ids, in the uuidv7 shape the schema generates, so a failure names a row
// rather than a value that changed between runs.
const (
	testAuthorID = "10000000-0000-7000-8000-0000000000a1"

	testGamePublishedNewer = "60000000-0000-7000-8000-0000000000a1"
	testGamePublishedOlder = "60000000-0000-7000-8000-0000000000a2"
	testGameDraft          = "60000000-0000-7000-8000-0000000000a3"
	testGameVariant        = "60000000-0000-7000-8000-0000000000a4"
	testGamePublishedThird = "60000000-0000-7000-8000-0000000000a5"

	// testGameMissing is well formed and deliberately never inserted.
	testGameMissing = "60000000-0000-7000-8000-0000000000ff"

	// Association fixtures. The prefixes match the seed data: 2 families,
	// 3 categories, 4 locations.
	testFamilyPurpose = "20000000-0000-7000-8000-0000000000a1"
	testFamilyEnergy  = "20000000-0000-7000-8000-0000000000a2"

	testCategoryIcebreaker = "30000000-0000-7000-8000-0000000000a1"
	testCategoryActive     = "30000000-0000-7000-8000-0000000000a2"

	testLocationIndoor  = "40000000-0000-7000-8000-0000000000a1"
	testLocationOutdoor = "40000000-0000-7000-8000-0000000000a2"

	// Well formed, deliberately never inserted: the filter has to reject
	// these rather than answer an empty list.
	testCategoryMissing = "30000000-0000-7000-8000-0000000000ff"
	testLocationMissing = "40000000-0000-7000-8000-0000000000ff"

	testMaterialRope      = "50000000-0000-7000-8000-0000000000a1"
	testMaterialBlindfold = "50000000-0000-7000-8000-0000000000a2"

	// Deliberately not in position order: the id is a uuidv7, so an id sort
	// and an insertion-time sort are the same sort, and a blocks query that
	// forgot its ORDER BY would still look right if these lined up.
	testBlockHeading = "70000000-0000-7000-8000-0000000000a3"
	testBlockIntro   = "70000000-0000-7000-8000-0000000000a1"
	testBlockSteps   = "70000000-0000-7000-8000-0000000000a2"
)

// testGame mirrors the columns the list endpoint reads, plus the two that
// decide whether a row should appear at all.
type testGame struct {
	ID              string
	Title           string
	Description     string
	Image           *string
	MinParticipants *int32
	MaxParticipants *int32
	DurationMin     *int32
	DurationMax     *int32
	NoMaterials     bool
	PublishState    string
	OriginalID      *string
	CreatedAt       time.Time

	// Authors is optional: a game left empty gets testAuthorID, so the
	// tests that do not care about authorship stay as they are. Name two
	// or more to exercise the many-to-many.
	Authors []string
}

func ptr[T any](v T) *T { return &v }

// insertAuthor writes a user for insertGame to credit. Every game needs at
// least one: game_authors carries the link, and a deferred trigger rejects a
// game that reaches commit without one.
func insertAuthor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string) {
	t.Helper()

	insertNamedAuthor(t, ctx, pool, id, "Test Author")
}

// insertNamedAuthor is insertAuthor for the tests that assert on author order,
// which need names that differ from each other.
func insertNamedAuthor(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	id, name string,
) {
	t.Helper()

	_, err := pool.Exec(ctx, `
		INSERT INTO users (id, name, email, pass_hash)
		VALUES ($1, $2, $3, 'not-a-real-hash')`,
		id, name, id+"@example.test")
	if err != nil {
		t.Fatalf("could not insert the author %s: %v", name, err)
	}
}

// insertGame writes one row with created_at set explicitly. now() is the
// transaction timestamp, so rows inserted by separate statements in the same
// test could otherwise tie and leave the expected order unverifiable.
func insertGame(t *testing.T, ctx context.Context, pool *pgxpool.Pool, g testGame) {
	t.Helper()

	authors := g.Authors
	if len(authors) == 0 {
		authors = []string{testAuthorID}
	}

	// The game and its authors go in together: the trigger that requires an
	// author is deferred to commit, so a game inserted on its own would be
	// rejected the moment the statement's own transaction ended.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("could not begin the transaction for game %s: %v", g.Title, err)
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO games (
			id, title, description, image,
			min_participants, max_participants, duration_min, duration_max,
			no_materials, publish_state, original_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		g.ID, g.Title, g.Description, g.Image,
		g.MinParticipants, g.MaxParticipants, g.DurationMin, g.DurationMax,
		g.NoMaterials, g.PublishState, g.OriginalID, g.CreatedAt)
	if err != nil {
		t.Fatalf("could not insert game %s: %v", g.Title, err)
	}

	for _, authorID := range authors {
		_, err = tx.Exec(ctx, `
			INSERT INTO game_authors (game_id, user_id) VALUES ($1, $2)`,
			g.ID, authorID)
		if err != nil {
			t.Fatalf("could not credit %s on game %s: %v", authorID, g.Title, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("could not commit game %s: %v", g.Title, err)
	}
}

// insertCategory writes a category and its family, so a test naming one
// category does not have to spell out the family row that categories.family_id
// requires.
func insertCategory(t *testing.T, ctx context.Context, pool *pgxpool.Pool, c gameCategory, familyOrder int32) {
	t.Helper()

	_, err := pool.Exec(ctx, `
		INSERT INTO category_families (id, name, display_order)
		VALUES ($1, $2, $3)
		ON CONFLICT (id) DO NOTHING`,
		c.FamilyID, c.FamilyName, familyOrder)
	if err != nil {
		t.Fatalf("could not insert the family %s: %v", c.FamilyName, err)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO categories (id, family_id, name, description, active, display_order)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		c.ID, c.FamilyID, c.Name, c.Description, c.Active, c.DisplayOrder)
	if err != nil {
		t.Fatalf("could not insert the category %s: %v", c.Name, err)
	}
}

func insertLocation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, l gameLocation) {
	t.Helper()

	_, err := pool.Exec(ctx, `INSERT INTO locations (id, name) VALUES ($1, $2)`, l.ID, l.Name)
	if err != nil {
		t.Fatalf("could not insert the location %s: %v", l.Name, err)
	}
}

func linkGameCategory(t *testing.T, ctx context.Context, pool *pgxpool.Pool, gameID, categoryID string) {
	t.Helper()

	_, err := pool.Exec(ctx, `
		INSERT INTO game_categories (game_id, category_id) VALUES ($1, $2)`,
		gameID, categoryID)
	if err != nil {
		t.Fatalf("could not link category %s to game %s: %v", categoryID, gameID, err)
	}
}

func linkGameLocation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, gameID, locationID string) {
	t.Helper()

	_, err := pool.Exec(ctx, `
		INSERT INTO game_locations (game_id, location_id) VALUES ($1, $2)`,
		gameID, locationID)
	if err != nil {
		t.Fatalf("could not link location %s to game %s: %v", locationID, gameID, err)
	}
}

func insertMaterial(t *testing.T, ctx context.Context, pool *pgxpool.Pool, m gameMaterial) {
	t.Helper()

	_, err := pool.Exec(ctx, `
		INSERT INTO materials (id, name, description) VALUES ($1, $2, $3)`,
		m.ID, m.Name, m.Description)
	if err != nil {
		t.Fatalf("could not insert the material %s: %v", m.Name, err)
	}
}

// linkGameMaterial writes the join row, which unlike the other two carries a
// payload: the quantities and the optional flag come from the link, not the
// material.
func linkGameMaterial(t *testing.T, ctx context.Context, pool *pgxpool.Pool, gameID string, m gameMaterial) {
	t.Helper()

	_, err := pool.Exec(ctx, `
		INSERT INTO game_materials (
			game_id, material_id, quantity_base, quantity_per_participant, optional)
		VALUES ($1, $2, $3, $4, $5)`,
		gameID, m.ID, m.QuantityBase, m.QuantityPerParticipant, m.Optional)
	if err != nil {
		t.Fatalf("could not link material %s to game %s: %v", m.ID, gameID, err)
	}
}

// insertBlock writes one block. Blocks hang off the game directly rather than
// through a join, so the game id is a column here and not a second row.
func insertBlock(t *testing.T, ctx context.Context, pool *pgxpool.Pool, gameID string, b gameBlock) {
	t.Helper()

	_, err := pool.Exec(ctx, `
		INSERT INTO blocks (id, game_id, type, content, position)
		VALUES ($1, $2, $3, $4, $5)`,
		b.ID, gameID, b.Type, b.Content, b.Position)
	if err != nil {
		t.Fatalf("could not insert block %s into game %s: %v", b.ID, gameID, err)
	}
}

// getGames performs the unfiltered request.
func getGames(t *testing.T, baseURL string) gamesListResponse {
	t.Helper()

	return getGamesFiltered(t, baseURL, "")
}

// getGamesFiltered performs the request and decodes the body, failing on
// anything but a 200 with a JSON content type.
func getGamesFiltered(t *testing.T, baseURL, rawQuery string) gamesListResponse {
	t.Helper()

	status, contentType, raw := getGamesRaw(t, baseURL, rawQuery)

	if status != http.StatusOK {
		t.Fatalf("GET /v1/games?%s status = %d, want %d (body %s)",
			rawQuery, status, http.StatusOK, raw)
	}
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want %q", contentType, "application/json")
	}

	var body gamesListResponse
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	return body
}

// getGamesRaw returns the response untouched, so a test can assert on a status
// the decoding helper would have failed on.
//
// The querystring is passed raw rather than as url.Values: the filter is about
// repeated keys and malformed values, and Values would normalise both.
func getGamesRaw(t *testing.T, baseURL, rawQuery string) (int, string, []byte) {
	t.Helper()

	url := baseURL + "/v1/games"
	if rawQuery != "" {
		url += "?" + rawQuery
	}

	res, err := authedGet(t, url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("could not read the response body: %v", err)
	}

	return res.StatusCode, res.Header.Get("Content-Type"), raw
}

// assertGameIDs compares the listed ids in order, since the endpoint promises
// newest first and a filter must not disturb that.
func assertGameIDs(t *testing.T, body gamesListResponse, want ...string) {
	t.Helper()

	got := make([]string, len(body.Games))
	for i, g := range body.Games {
		got[i] = g.ID
	}

	if len(got) != len(want) {
		t.Fatalf("got %d games %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("games[%d].id = %s, want %s (full order %v)", i, got[i], want[i], got)
		}
	}
}

func assertSummary(t *testing.T, got, want gameSummary) {
	t.Helper()

	if got.ID != want.ID {
		t.Errorf("id = %s, want %s", got.ID, want.ID)
	}
	if got.Title != want.Title {
		t.Errorf("title = %q, want %q", got.Title, want.Title)
	}
	if got.Description != want.Description {
		t.Errorf("description = %q, want %q", got.Description, want.Description)
	}
	if got.NoMaterials != want.NoMaterials {
		t.Errorf("no_materials = %t, want %t", got.NoMaterials, want.NoMaterials)
	}

	assertStringPtr(t, "image", got.Image, want.Image)
	assertInt32Ptr(t, "min_participants", got.MinParticipants, want.MinParticipants)
	assertInt32Ptr(t, "max_participants", got.MaxParticipants, want.MaxParticipants)
	assertInt32Ptr(t, "duration_min", got.DurationMin, want.DurationMin)
	assertInt32Ptr(t, "duration_max", got.DurationMax, want.DurationMax)
	assertAuthors(t, got.Authors, want.Authors)
}

// assertAuthors compares the credit line in order: the order is the feature,
// so a right set in the wrong sequence is a failure.
func assertAuthors(t *testing.T, got, want []gameAuthor) {
	t.Helper()

	if got == nil {
		t.Errorf("authors = null, want an array")
		return
	}

	if len(got) != len(want) {
		t.Errorf("authors = %v, want %v", authorNames(got), authorNames(want))
		return
	}

	for i := range want {
		if got[i].ID != want[i].ID || got[i].Name != want[i].Name {
			t.Errorf("authors[%d] = %s (%s), want %s (%s); full order %v",
				i, got[i].Name, got[i].ID, want[i].Name, want[i].ID, authorNames(got))
		}
		assertStringPtr(t, "authors["+got[i].ID+"].img", got[i].Img, want[i].Img)
	}
}

func authorNames(authors []gameAuthor) []string {
	names := make([]string, len(authors))
	for i, a := range authors {
		names[i] = a.Name
	}

	return names
}

// testAuthors is the credit line every game gets from the plain insertAuthor
// helper, which the card-shape tests expect to find on the wire.
func testAuthors() []gameAuthor {
	return []gameAuthor{{ID: testAuthorID, Name: "Test Author"}}
}

func assertStringPtr(t *testing.T, field string, got, want *string) {
	t.Helper()

	switch {
	case got == nil && want == nil:
	case got == nil || want == nil:
		t.Errorf("%s = %v, want %v", field, deref(got), deref(want))
	case *got != *want:
		t.Errorf("%s = %q, want %q", field, *got, *want)
	}
}

func assertInt32Ptr(t *testing.T, field string, got, want *int32) {
	t.Helper()

	switch {
	case got == nil && want == nil:
	case got == nil || want == nil:
		t.Errorf("%s = %v, want %v", field, deref(got), deref(want))
	case *got != *want:
		t.Errorf("%s = %d, want %d", field, *got, *want)
	}
}

// deref renders a pointer for an error message, printing a nil as "null" to
// match how it appears in the response body.
func deref[T any](p *T) any {
	if p == nil {
		return "null"
	}
	return *p
}
