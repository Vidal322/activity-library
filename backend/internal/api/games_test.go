package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
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
}

func ptr[T any](v T) *T { return &v }

// insertAuthor writes the user every game needs, since games.author_id is NOT
// NULL with a RESTRICT reference.
func insertAuthor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string) {
	t.Helper()

	_, err := pool.Exec(ctx, `
		INSERT INTO users (id, name, email, pass_hash)
		VALUES ($1, 'Test Author', $2, 'not-a-real-hash')`,
		id, id+"@example.test")
	if err != nil {
		t.Fatalf("could not insert the author: %v", err)
	}
}

// insertGame writes one row with created_at set explicitly. now() is the
// transaction timestamp, so rows inserted by separate statements in the same
// test could otherwise tie and leave the expected order unverifiable.
func insertGame(t *testing.T, ctx context.Context, pool *pgxpool.Pool, g testGame) {
	t.Helper()

	_, err := pool.Exec(ctx, `
		INSERT INTO games (
			id, title, description, image,
			min_participants, max_participants, duration_min, duration_max,
			no_materials, publish_state, author_id, original_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		g.ID, g.Title, g.Description, g.Image,
		g.MinParticipants, g.MaxParticipants, g.DurationMin, g.DurationMax,
		g.NoMaterials, g.PublishState, testAuthorID, g.OriginalID, g.CreatedAt)
	if err != nil {
		t.Fatalf("could not insert game %s: %v", g.Title, err)
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

	res, err := http.Get(url)
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

// TestHandleGamesListReturnsOnlyPublished is the assertion the endpoint exists
// for: a draft is invisible, whatever else is in the table.
func TestHandleGamesListReturnsOnlyPublished(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedOlder,
		Title:           "Older published",
		Description:     "written first",
		MinParticipants: ptr(int32(4)),
		MaxParticipants: ptr(int32(12)),
		DurationMin:     ptr(int32(10)),
		DurationMax:     ptr(int32(20)),
		PublishState:    "published",
		CreatedAt:       base,
	})
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "Newer published",
		Description:     "written second",
		Image:           ptr("https://example.test/newer.png"),
		MinParticipants: ptr(int32(2)),
		MaxParticipants: ptr(int32(8)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(15)),
		NoMaterials:     true,
		PublishState:    "published",
		CreatedAt:       base.Add(time.Hour),
	})
	insertGame(t, ctx, pool, testGame{
		ID:              testGameDraft,
		Title:           "Still a draft",
		Description:     "must not be listed",
		MinParticipants: ptr(int32(3)),
		MaxParticipants: ptr(int32(6)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(10)),
		PublishState:    "draft",
		CreatedAt:       base.Add(2 * time.Hour),
	})

	body := getGames(t, srv.URL)

	gotIDs := make([]string, len(body.Games))
	for i, g := range body.Games {
		gotIDs[i] = g.ID
	}

	// Newest first, so the second row inserted leads.
	wantIDs := []string{testGamePublishedNewer, testGamePublishedOlder}
	if len(gotIDs) != len(wantIDs) {
		t.Fatalf("got %d games %v, want %d %v", len(gotIDs), gotIDs, len(wantIDs), wantIDs)
	}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Errorf("games[%d].id = %s, want %s (full order %v)", i, gotIDs[i], wantIDs[i], gotIDs)
		}
	}
}

// TestHandleGamesListMapsCardFields pins the wire shape field by field,
// including the two that are easiest to get wrong: a null image and the
// no_materials flag.
func TestHandleGamesListMapsCardFields(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)

	want := gameSummary{
		ID:              testGamePublishedNewer,
		Title:           "Human Knot",
		Description:     "Untangle the circle without letting go.",
		Image:           nil,
		MinParticipants: ptr(int32(6)),
		MaxParticipants: ptr(int32(16)),
		DurationMin:     ptr(int32(10)),
		DurationMax:     ptr(int32(15)),
		NoMaterials:     true,
	}

	insertGame(t, ctx, pool, testGame{
		ID:              want.ID,
		Title:           want.Title,
		Description:     want.Description,
		Image:           want.Image,
		MinParticipants: want.MinParticipants,
		MaxParticipants: want.MaxParticipants,
		DurationMin:     want.DurationMin,
		DurationMax:     want.DurationMax,
		NoMaterials:     want.NoMaterials,
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	})

	body := getGames(t, srv.URL)
	if len(body.Games) != 1 {
		t.Fatalf("got %d games, want 1", len(body.Games))
	}

	assertSummary(t, body.Games[0], want)
}

// TestHandleGamesListKeepsVariantNullsNull covers the case the schema allows
// and a zero value would misreport: a variant carries no participants or
// duration of its own, and the card must say so rather than claim zero.
func TestHandleGamesListKeepsVariantNullsNull(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)

	original := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "The original",
		MinParticipants: ptr(int32(6)),
		MaxParticipants: ptr(int32(16)),
		DurationMin:     ptr(int32(10)),
		DurationMax:     ptr(int32(15)),
		PublishState:    "published",
		CreatedAt:       original,
	})
	insertGame(t, ctx, pool, testGame{
		ID:           testGameVariant,
		Title:        "A variant with nothing of its own",
		PublishState: "published",
		OriginalID:   ptr(testGamePublishedNewer),
		CreatedAt:    original.Add(time.Hour),
	})

	body := getGames(t, srv.URL)
	if len(body.Games) != 2 {
		t.Fatalf("got %d games, want 2", len(body.Games))
	}

	assertSummary(t, body.Games[0], gameSummary{
		ID:    testGameVariant,
		Title: "A variant with nothing of its own",
	})
}

// TestHandleGamesListReturnsEmptyArray guards the make-with-zero-length in the
// handler: a nil slice would marshal to null, which no client should have to
// branch on.
func TestHandleGamesListReturnsEmptyArray(t *testing.T) {
	srv, _ := newTestServer(t)

	res, err := http.Get(srv.URL + "/v1/games")
	if err != nil {
		t.Fatalf("GET /v1/games: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusOK)
	}

	// Decoded into RawMessage rather than the response struct: null and []
	// both decode to a nil slice, so only the bytes tell them apart.
	var body struct {
		Games json.RawMessage `json:"games"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	if got := string(body.Games); got != "[]" {
		t.Errorf("games = %s, want []", got)
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

// getGame performs the request and hands back the status alongside the body, so
// the error-path tests can assert on a 404 or a 400 rather than fataling the
// way getGames does.
func getGame(t *testing.T, baseURL, id string) (int, []byte) {
	t.Helper()

	res, err := http.Get(baseURL + "/v1/games/" + id)
	if err != nil {
		t.Fatalf("GET /v1/games/%s: %v", id, err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("could not read the response body: %v", err)
	}

	return res.StatusCode, body
}

// TestHandleGetGameReturnsTheRow pins the wire shape of the happy path. It is
// the same envelope-free summary the list endpoint emits, so a client reading a
// card and a client reading a game see one shape.
func TestHandleGetGameReturnsTheRow(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)

	want := gameSummary{
		ID:              testGamePublishedNewer,
		Title:           "Human Knot",
		Description:     "Untangle the circle without letting go.",
		Image:           ptr("https://example.test/knot.png"),
		MinParticipants: ptr(int32(6)),
		MaxParticipants: ptr(int32(16)),
		DurationMin:     ptr(int32(10)),
		DurationMax:     ptr(int32(15)),
		NoMaterials:     true,
	}

	// A second row the request must not return, so a query that ignores its
	// argument fails here instead of passing by luck.
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedOlder,
		Title:           "Not the one asked for",
		MinParticipants: ptr(int32(2)),
		MaxParticipants: ptr(int32(4)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(10)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	})
	insertGame(t, ctx, pool, testGame{
		ID:              want.ID,
		Title:           want.Title,
		Description:     want.Description,
		Image:           want.Image,
		MinParticipants: want.MinParticipants,
		MaxParticipants: want.MaxParticipants,
		DurationMin:     want.DurationMin,
		DurationMax:     want.DurationMax,
		NoMaterials:     want.NoMaterials,
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC),
	})

	status, body := getGame(t, srv.URL, want.ID)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, body)
	}

	var got gameSummary
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	assertSummary(t, got, want)
}

// TestHandleGetGameKeepsVariantNullsNull is the null-vs-zero trap the list
// endpoint also guards: a variant carries no participants or duration of its
// own, and the response must say null rather than claim zero.
func TestHandleGetGameKeepsVariantNullsNull(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)

	original := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "The original",
		MinParticipants: ptr(int32(6)),
		MaxParticipants: ptr(int32(16)),
		DurationMin:     ptr(int32(10)),
		DurationMax:     ptr(int32(15)),
		PublishState:    "published",
		CreatedAt:       original,
	})
	insertGame(t, ctx, pool, testGame{
		ID:           testGameVariant,
		Title:        "A variant with nothing of its own",
		PublishState: "published",
		OriginalID:   ptr(testGamePublishedNewer),
		CreatedAt:    original.Add(time.Hour),
	})

	status, body := getGame(t, srv.URL, testGameVariant)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, body)
	}

	var got gameSummary
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	assertSummary(t, got, gameSummary{
		ID:    testGameVariant,
		Title: "A variant with nothing of its own",
	})
}

// TestHandleGetGameUnknownID is the path that only works because the store
// collects exactly one row: an empty result has to surface as pgx.ErrNoRows for
// classify to turn it into ErrNotFound, and a slice-collecting query would
// panic or return a zero value instead.
func TestHandleGetGameUnknownID(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "The only game in the table",
		MinParticipants: ptr(int32(2)),
		MaxParticipants: ptr(int32(4)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(10)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	})

	status, body := getGame(t, srv.URL, testGameMissing)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusNotFound, body)
	}
}

// TestHandleGetGameMalformedID guards the reason the handler parses the id
// before querying: Postgres answers a malformed uuid with 22P02, which classify
// does not recognise and writeStoreError would report as a 500.
func TestHandleGetGameMalformedID(t *testing.T) {
	srv, _ := newTestServer(t)

	for _, id := range []string{
		"not-a-uuid",
		"60000000-0000-7000-8000-0000000000zz",
		"60000000-0000-7000-8000",
	} {
		t.Run(id, func(t *testing.T) {
			status, body := getGame(t, srv.URL, id)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body %s)", status, http.StatusBadRequest, body)
			}

			var got map[string]string
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("could not decode the response body: %v", err)
			}
			if got["error"] == "" {
				t.Errorf("body = %s, want an error message", body)
			}
		})
	}
}

// TestHandleGetGameIncludesAssociations is what the detail payload exists for:
// both join tables come back on the game, and each category carries the family
// it belongs to.
func TestHandleGetGameIncludesAssociations(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "Human Knot",
		MinParticipants: ptr(int32(6)),
		MaxParticipants: ptr(int32(16)),
		DurationMin:     ptr(int32(10)),
		DurationMax:     ptr(int32(15)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	})

	// The energetic category is written first and belongs to the family with
	// the higher display_order, so a response in insertion order fails here.
	active := gameCategory{
		ID:           testCategoryActive,
		Name:         "Active",
		Description:  "Gets everyone moving.",
		Active:       true,
		DisplayOrder: 1,
		FamilyID:     testFamilyEnergy,
		FamilyName:   "Energy",
	}
	icebreaker := gameCategory{
		ID:           testCategoryIcebreaker,
		Name:         "Icebreaker",
		Description:  "Opens a group that has just met.",
		Active:       true,
		DisplayOrder: 2,
		FamilyID:     testFamilyPurpose,
		FamilyName:   "Purpose",
	}
	indoor := gameLocation{ID: testLocationIndoor, Name: "Indoor"}

	insertCategory(t, ctx, pool, active, 2)
	insertCategory(t, ctx, pool, icebreaker, 1)
	insertLocation(t, ctx, pool, indoor)

	linkGameCategory(t, ctx, pool, testGamePublishedNewer, active.ID)
	linkGameCategory(t, ctx, pool, testGamePublishedNewer, icebreaker.ID)
	linkGameLocation(t, ctx, pool, testGamePublishedNewer, indoor.ID)

	// A second game holding the same rows, so a query that ignores its
	// argument and returns every link fails here.
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedOlder,
		Title:           "Not the one asked for",
		MinParticipants: ptr(int32(2)),
		MaxParticipants: ptr(int32(4)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(10)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC),
	})
	linkGameCategory(t, ctx, pool, testGamePublishedOlder, active.ID)
	linkGameLocation(t, ctx, pool, testGamePublishedOlder, indoor.ID)

	status, body := getGame(t, srv.URL, testGamePublishedNewer)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, body)
	}

	var got gameDetail
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	want := []gameCategory{icebreaker, active}
	if len(got.Categories) != len(want) {
		t.Fatalf("got %d categories %+v, want %d", len(got.Categories), got.Categories, len(want))
	}
	for i := range want {
		if got.Categories[i] != want[i] {
			t.Errorf("categories[%d] = %+v, want %+v", i, got.Categories[i], want[i])
		}
	}

	if len(got.Locations) != 1 {
		t.Fatalf("got %d locations %+v, want 1", len(got.Locations), got.Locations)
	}
	if got.Locations[0] != indoor {
		t.Errorf("locations[0] = %+v, want %+v", got.Locations[0], indoor)
	}
}

// TestHandleGetGameEmptyAssociations guards the make-with-zero-length in
// newGameDetail: a game with no links must answer with [] rather than null, so
// the frontend never branches on the difference.
func TestHandleGetGameEmptyAssociations(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "Unclassified",
		MinParticipants: ptr(int32(2)),
		MaxParticipants: ptr(int32(4)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(10)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	})

	status, body := getGame(t, srv.URL, testGamePublishedNewer)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, body)
	}

	// RawMessage rather than the response struct: null and [] both decode to a
	// nil slice, so only the bytes tell them apart.
	var got struct {
		Categories json.RawMessage `json:"categories"`
		Locations  json.RawMessage `json:"locations"`
		Materials  json.RawMessage `json:"materials"`
		Blocks     json.RawMessage `json:"blocks"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	if s := string(got.Categories); s != "[]" {
		t.Errorf("categories = %s, want []", s)
	}
	if s := string(got.Locations); s != "[]" {
		t.Errorf("locations = %s, want []", s)
	}
	if s := string(got.Materials); s != "[]" {
		t.Errorf("materials = %s, want []", s)
	}
	if s := string(got.Blocks); s != "[]" {
		t.Errorf("blocks = %s, want []", s)
	}
}

// TestHandleGetGameIncludesMaterialQuantities is the round trip issue #16 asks
// for: quantity_base, quantity_per_participant and optional are what make a kit
// list renderable, and all three live on the join row rather than the material,
// so a query that forgot to select them still returns a plausible-looking name.
func TestHandleGetGameIncludesMaterialQuantities(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "Blindfold Maze",
		MinParticipants: ptr(int32(4)),
		MaxParticipants: ptr(int32(12)),
		DurationMin:     ptr(int32(10)),
		DurationMax:     ptr(int32(20)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	})

	// The three columns are given three different values per material, so a
	// mapping that crossed two of them fails here. Rope is written first and
	// sorts second, so a response in insertion order fails too.
	rope := gameMaterial{
		ID:                     testMaterialRope,
		Name:                   "Rope",
		Description:            "Roughly 10 metres, soft enough to hold.",
		QuantityBase:           2,
		QuantityPerParticipant: 0,
		Optional:               true,
	}
	blindfold := gameMaterial{
		ID:                     testMaterialBlindfold,
		Name:                   "Blindfold",
		Description:            "An opaque cloth or sleep mask.",
		QuantityBase:           0,
		QuantityPerParticipant: 1,
		Optional:               false,
	}

	insertMaterial(t, ctx, pool, rope)
	insertMaterial(t, ctx, pool, blindfold)
	linkGameMaterial(t, ctx, pool, testGamePublishedNewer, rope)
	linkGameMaterial(t, ctx, pool, testGamePublishedNewer, blindfold)

	// A second game holding the same material at a different quantity, so a
	// query that ignores its argument fails rather than passing by coincidence.
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedOlder,
		Title:           "Not the one asked for",
		MinParticipants: ptr(int32(2)),
		MaxParticipants: ptr(int32(4)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(10)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC),
	})
	other := rope
	other.QuantityBase = 99
	linkGameMaterial(t, ctx, pool, testGamePublishedOlder, other)

	status, body := getGame(t, srv.URL, testGamePublishedNewer)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, body)
	}

	var got gameDetail
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	want := []gameMaterial{blindfold, rope}
	if len(got.Materials) != len(want) {
		t.Fatalf("got %d materials %+v, want %d", len(got.Materials), got.Materials, len(want))
	}
	for i := range want {
		if got.Materials[i] != want[i] {
			t.Errorf("materials[%d] = %+v, want %+v", i, got.Materials[i], want[i])
		}
	}

	if got.NoMaterials {
		t.Errorf("no_materials = true, want false for a game that needs materials")
	}
}

// TestHandleGetGameNoMaterialsIsDistinguishable covers the other half of issue
// #16: "needs nothing" and "nobody has filled the list in yet" are different
// states, and both answer with an empty array. Only the flag separates them, so
// a client that reads the array alone cannot tell a deliberate empty kit from
// an incomplete record.
func TestHandleGetGameNoMaterialsIsDistinguishable(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)

	// Declared to need nothing.
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "Circle of Words",
		MinParticipants: ptr(int32(4)),
		MaxParticipants: ptr(int32(20)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(10)),
		NoMaterials:     true,
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	})

	// Not declared, and nothing linked yet.
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedOlder,
		Title:           "Unfinished",
		MinParticipants: ptr(int32(2)),
		MaxParticipants: ptr(int32(4)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(10)),
		NoMaterials:     false,
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC),
	})

	for _, tc := range []struct {
		name           string
		gameID         string
		wantNoMaterial bool
	}{
		{"declared to need nothing", testGamePublishedNewer, true},
		{"list not filled in yet", testGamePublishedOlder, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := getGame(t, srv.URL, tc.gameID)
			if status != http.StatusOK {
				t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, body)
			}

			// RawMessage for the array, for the reason
			// TestHandleGetGameEmptyAssociations gives: null and [] both
			// decode to a nil slice.
			var got struct {
				NoMaterials bool            `json:"no_materials"`
				Materials   json.RawMessage `json:"materials"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("could not decode the response body: %v", err)
			}

			if s := string(got.Materials); s != "[]" {
				t.Errorf("materials = %s, want []", s)
			}
			if got.NoMaterials != tc.wantNoMaterial {
				t.Errorf("no_materials = %t, want %t", got.NoMaterials, tc.wantNoMaterial)
			}
		})
	}
}

// TestHandleGetGameIncludesBlocksInPositionOrder is the round trip issue #17
// asks for: blocks are a document, and position is the only thing that makes
// them one. They are written here in the order 2, 0, 1, so a query that leans
// on insertion order — or on the id, which is a uuidv7 and therefore sorts by
// insertion time — fails rather than passing by coincidence.
func TestHandleGetGameIncludesBlocksInPositionOrder(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "Human Knot",
		MinParticipants: ptr(int32(6)),
		MaxParticipants: ptr(int32(16)),
		DurationMin:     ptr(int32(10)),
		DurationMax:     ptr(int32(15)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	})

	heading := gameBlock{
		ID:       testBlockHeading,
		Type:     "heading",
		Content:  "Como jogar",
		Position: 0,
	}
	intro := gameBlock{
		ID:       testBlockIntro,
		Type:     "paragraph",
		Content:  "O grupo forma um círculo apertado.",
		Position: 1,
	}
	steps := gameBlock{
		ID:       testBlockSteps,
		Type:     "steps",
		Content:  "1. Dar as mãos.\n2. Desfazer o nó.",
		Position: 2,
	}

	insertBlock(t, ctx, pool, testGamePublishedNewer, steps)
	insertBlock(t, ctx, pool, testGamePublishedNewer, heading)
	insertBlock(t, ctx, pool, testGamePublishedNewer, intro)

	// A second game whose block sits at position 0, so a query that ignores its
	// argument would sort it to the front and fail here.
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedOlder,
		Title:           "Not the one asked for",
		MinParticipants: ptr(int32(2)),
		MaxParticipants: ptr(int32(4)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(10)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC),
	})
	insertBlock(t, ctx, pool, testGamePublishedOlder, gameBlock{
		ID:       "70000000-0000-7000-8000-0000000000ff",
		Type:     "paragraph",
		Content:  "Belongs to another game.",
		Position: 0,
	})

	status, body := getGame(t, srv.URL, testGamePublishedNewer)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, body)
	}

	var got gameDetail
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	want := []gameBlock{heading, intro, steps}
	if len(got.Blocks) != len(want) {
		t.Fatalf("got %d blocks %+v, want %d", len(got.Blocks), got.Blocks, len(want))
	}
	for i := range want {
		if got.Blocks[i] != want[i] {
			t.Errorf("blocks[%d] = %+v, want %+v", i, got.Blocks[i], want[i])
		}
	}
}

// TestHandleGetGameOmitsBlockSearchColumn holds the other half of issue #17:
// blocks.search is a generated tsvector, an index artefact no client has any
// use for. Decoding into gameDetail would drop an extra key silently, so the
// assertion is on the raw keys the handler actually wrote.
func TestHandleGetGameOmitsBlockSearchColumn(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "Human Knot",
		MinParticipants: ptr(int32(6)),
		MaxParticipants: ptr(int32(16)),
		DurationMin:     ptr(int32(10)),
		DurationMax:     ptr(int32(15)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	})
	insertBlock(t, ctx, pool, testGamePublishedNewer, gameBlock{
		ID:       testBlockIntro,
		Type:     "paragraph",
		Content:  "O grupo forma um círculo apertado.",
		Position: 0,
	})

	status, body := getGame(t, srv.URL, testGamePublishedNewer)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, body)
	}

	var got struct {
		Blocks []map[string]json.RawMessage `json:"blocks"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}
	if len(got.Blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(got.Blocks))
	}

	wantKeys := []string{"id", "type", "content", "position"}
	if len(got.Blocks[0]) != len(wantKeys) {
		t.Errorf("blocks[0] has keys %v, want exactly %v", keysOf(got.Blocks[0]), wantKeys)
	}
	for _, k := range wantKeys {
		if _, ok := got.Blocks[0][k]; !ok {
			t.Errorf("blocks[0] is missing the key %q", k)
		}
	}
	if _, ok := got.Blocks[0]["search"]; ok {
		t.Errorf("blocks[0] carries the generated search column")
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// seedFilterFixture writes the rows the filter tests read: three published
// games with overlapping categories and locations, plus a draft that carries
// every association and is the newest row, so a filter that forgot the publish
// state would lead its list with it.
//
//	newer  Icebreaker, Active   Indoor
//	third  Active               Indoor, Outdoor
//	older  Icebreaker           Outdoor
//	draft  Icebreaker, Active   Indoor, Outdoor
func seedFilterFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	insertAuthor(t, ctx, pool, testAuthorID)

	insertCategory(t, ctx, pool, gameCategory{
		ID:           testCategoryIcebreaker,
		Name:         "Icebreaker",
		Description:  "Helps a new group get talking.",
		Active:       true,
		DisplayOrder: 0,
		FamilyID:     testFamilyPurpose,
		FamilyName:   "Purpose",
	}, 0)
	insertCategory(t, ctx, pool, gameCategory{
		ID:           testCategoryActive,
		Name:         "Active",
		Description:  "Raises the energy of the group.",
		Active:       true,
		DisplayOrder: 0,
		FamilyID:     testFamilyEnergy,
		FamilyName:   "Energy",
	}, 1)

	insertLocation(t, ctx, pool, gameLocation{ID: testLocationIndoor, Name: "Indoor"})
	insertLocation(t, ctx, pool, gameLocation{ID: testLocationOutdoor, Name: "Outdoor"})

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	games := []struct {
		id           string
		title        string
		publishState string
		createdAt    time.Time
		categories   []string
		locations    []string
	}{
		{
			id:           testGamePublishedOlder,
			title:        "Circle of Words",
			publishState: "published",
			createdAt:    base,
			categories:   []string{testCategoryIcebreaker},
			locations:    []string{testLocationOutdoor},
		},
		{
			id:           testGamePublishedThird,
			title:        "Blindfold Maze",
			publishState: "published",
			createdAt:    base.Add(time.Hour),
			categories:   []string{testCategoryActive},
			locations:    []string{testLocationIndoor, testLocationOutdoor},
		},
		{
			id:           testGamePublishedNewer,
			title:        "Human Knot",
			publishState: "published",
			createdAt:    base.Add(2 * time.Hour),
			categories:   []string{testCategoryIcebreaker, testCategoryActive},
			locations:    []string{testLocationIndoor},
		},
		{
			id:           testGameDraft,
			title:        "Still a draft",
			publishState: "draft",
			createdAt:    base.Add(3 * time.Hour),
			categories:   []string{testCategoryIcebreaker, testCategoryActive},
			locations:    []string{testLocationIndoor, testLocationOutdoor},
		},
	}

	for _, g := range games {
		insertGame(t, ctx, pool, testGame{
			ID:              g.id,
			Title:           g.title,
			Description:     "a game the filter tests read",
			MinParticipants: ptr(int32(4)),
			MaxParticipants: ptr(int32(12)),
			DurationMin:     ptr(int32(10)),
			DurationMax:     ptr(int32(20)),
			PublishState:    g.publishState,
			CreatedAt:       g.createdAt,
		})

		for _, c := range g.categories {
			linkGameCategory(t, ctx, pool, g.id, c)
		}
		for _, l := range g.locations {
			linkGameLocation(t, ctx, pool, g.id, l)
		}
	}
}

// TestHandleGamesListFiltersByCategory is the base case: only the games
// carrying the category come back, still newest first, still without the draft.
func TestHandleGamesListFiltersByCategory(t *testing.T) {
	srv, pool := newTestServer(t)
	seedFilterFixture(t, testContext(t), pool)

	body := getGamesFiltered(t, srv.URL, "category="+testCategoryIcebreaker)

	assertGameIDs(t, body, testGamePublishedNewer, testGamePublishedOlder)
}

// TestHandleGamesListANDsCategories is the assertion the filter rail implies:
// two categories narrow the list rather than widening it, so the game carrying
// only one of them drops out.
func TestHandleGamesListANDsCategories(t *testing.T) {
	srv, pool := newTestServer(t)
	seedFilterFixture(t, testContext(t), pool)

	body := getGamesFiltered(t, srv.URL,
		"category="+testCategoryIcebreaker+"&category="+testCategoryActive)

	assertGameIDs(t, body, testGamePublishedNewer)
}

func TestHandleGamesListFiltersByLocation(t *testing.T) {
	srv, pool := newTestServer(t)
	seedFilterFixture(t, testContext(t), pool)

	body := getGamesFiltered(t, srv.URL, "location="+testLocationIndoor)

	assertGameIDs(t, body, testGamePublishedNewer, testGamePublishedThird)
}

// TestHandleGamesListCombinesCategoryAndLocation crosses the two fields. Each
// half alone matches two games, and only one game satisfies both.
func TestHandleGamesListCombinesCategoryAndLocation(t *testing.T) {
	srv, pool := newTestServer(t)
	seedFilterFixture(t, testContext(t), pool)

	body := getGamesFiltered(t, srv.URL,
		"category="+testCategoryActive+"&location="+testLocationOutdoor)

	assertGameIDs(t, body, testGamePublishedThird)
}

// TestHandleGamesListWithoutFilterIsUnchanged pins that the filter is opt-in:
// the parameters absent, the endpoint still lists every published game.
func TestHandleGamesListWithoutFilterIsUnchanged(t *testing.T) {
	srv, pool := newTestServer(t)
	seedFilterFixture(t, testContext(t), pool)

	body := getGames(t, srv.URL)

	assertGameIDs(t, body,
		testGamePublishedNewer, testGamePublishedThird, testGamePublishedOlder)
}

// TestHandleGamesListDeduplicatesFilterIDs guards the match itself: it counts
// join rows against the length of the requested array, and the join keys are
// unique, so a repeat would push the target out of reach and match nothing.
// The second case is the same id in the spelling pgtype also accepts, which a
// set keyed on the raw string would miss.
func TestHandleGamesListDeduplicatesFilterIDs(t *testing.T) {
	srv, pool := newTestServer(t)
	seedFilterFixture(t, testContext(t), pool)

	bare := strings.ReplaceAll(testCategoryIcebreaker, "-", "")

	for name, query := range map[string]string{
		"same spelling":      "category=" + testCategoryIcebreaker + "&category=" + testCategoryIcebreaker,
		"different spelling": "category=" + testCategoryIcebreaker + "&category=" + strings.ToUpper(bare),
	} {
		t.Run(name, func(t *testing.T) {
			body := getGamesFiltered(t, srv.URL, query)

			assertGameIDs(t, body, testGamePublishedNewer, testGamePublishedOlder)
		})
	}
}

// TestHandleGamesListRejectsMalformedFilterID keeps a bad id from reaching
// Postgres, which answers 22P02 for a malformed uuid; classify does not
// recognise that code and writeStoreError would report it as a 500.
func TestHandleGamesListRejectsMalformedFilterID(t *testing.T) {
	srv, pool := newTestServer(t)
	seedFilterFixture(t, testContext(t), pool)

	for name, query := range map[string]string{
		"not a uuid":        "category=nonsense",
		"truncated":         "location=40000000-0000-7000-8000",
		"empty value":       "category=",
		"bad among good":    "category=" + testCategoryIcebreaker + "&category=nonsense",
		"malformed on both": "category=nonsense&location=nonsense",
	} {
		t.Run(name, func(t *testing.T) {
			status, _, raw := getGamesRaw(t, srv.URL, query)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body %s)", status, http.StatusBadRequest, raw)
			}

			var got map[string]string
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("could not decode the response body: %v", err)
			}
			if got["error"] == "" {
				t.Errorf("body = %s, want an error message", raw)
			}
		})
	}
}

// TestHandleGamesListRejectsUnknownFilterID is the half a malformed-id check
// cannot cover: the id is a valid uuid that names no row. Answering an empty
// list would read as a library with nothing in it, so it is a 400 naming the
// id instead.
func TestHandleGamesListRejectsUnknownFilterID(t *testing.T) {
	srv, pool := newTestServer(t)
	seedFilterFixture(t, testContext(t), pool)

	for name, tc := range map[string]struct{ query, wantID string }{
		"category":         {"category=" + testCategoryMissing, testCategoryMissing},
		"location":         {"location=" + testLocationMissing, testLocationMissing},
		"among known ones": {"category=" + testCategoryIcebreaker + "&category=" + testCategoryMissing, testCategoryMissing},
	} {
		t.Run(name, func(t *testing.T) {
			status, _, raw := getGamesRaw(t, srv.URL, tc.query)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body %s)", status, http.StatusBadRequest, raw)
			}

			var got map[string]string
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("could not decode the response body: %v", err)
			}
			if !strings.Contains(got["error"], tc.wantID) {
				t.Errorf("error = %q, want it to name %s", got["error"], tc.wantID)
			}
		})
	}
}
