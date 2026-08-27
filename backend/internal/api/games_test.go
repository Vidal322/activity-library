package api

import (
	"context"
	"encoding/json"
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
