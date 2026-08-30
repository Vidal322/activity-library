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

	// testGameMissing is well formed and deliberately never inserted.
	testGameMissing = "60000000-0000-7000-8000-0000000000ff"

	// Association fixtures. The prefixes match the seed data: 2 families,
	// 3 categories, 4 locations.
	testFamilyPurpose = "20000000-0000-7000-8000-0000000000a1"
	testFamilyEnergy  = "20000000-0000-7000-8000-0000000000a2"

	testCategoryIcebreaker = "30000000-0000-7000-8000-0000000000a1"
	testCategoryActive     = "30000000-0000-7000-8000-0000000000a2"

	testLocationIndoor = "40000000-0000-7000-8000-0000000000a1"
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

// getGames performs the request and decodes the body, failing on anything but a
// 200 with a JSON content type.
func getGames(t *testing.T, baseURL string) gamesListResponse {
	t.Helper()

	res, err := http.Get(baseURL + "/v1/games")
	if err != nil {
		t.Fatalf("GET /v1/games: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/games status = %d, want %d", res.StatusCode, http.StatusOK)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}

	var body gamesListResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	return body
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
}
