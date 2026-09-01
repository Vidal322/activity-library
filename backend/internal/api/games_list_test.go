package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

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
