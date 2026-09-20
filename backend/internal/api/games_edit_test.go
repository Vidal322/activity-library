package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A partial update has three states per field, and the tests below are
// organised around proving all three for every column that can hold a null:
//
//	absent  the column keeps what it had
//	null    the column is cleared
//	value   the column takes the value
//
// The middle one is the reason the endpoint cannot be written with plain
// pointers, and it is also the one a naive implementation passes by accident
// for the wrong reason — a handler that ignored nulls entirely would satisfy
// "absent leaves the value" on both inputs. Every field is therefore checked in
// all three states rather than in whichever one is convenient.

// editableGame is the row the edit tests start from: a complete, published
// non-variant credited to the caller, so nothing in these tests turns on
// visibility.
const editableGame = testGamePublishedOlder

// seedEditFixture writes that row. The values are all distinct so an update
// that wrote the right number into the wrong column fails rather than passes.
func seedEditFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	insertGame(t, ctx, pool, testGame{
		ID:              editableGame,
		Title:           "Jogo do Lenço",
		Description:     "Two teams, one handkerchief",
		Image:           ptr("https://example.test/lenco.png"),
		MinParticipants: ptr(int32(6)),
		MaxParticipants: ptr(int32(30)),
		DurationMin:     ptr(int32(15)),
		DurationMax:     ptr(int32(40)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Authors:         []string{testSessionUserID},
	})
}

// seedEditableVariant writes a variant of the row above. The four spine columns
// are nullable only for a variant — games_non_variant_complete requires them
// everywhere else — so a variant is the only place "null clears it" can be
// demonstrated on them at all.
func seedEditableVariant(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	insertGame(t, ctx, pool, testGame{
		ID:              testGameVariant,
		Title:           "Jogo do Lenço, versão curta",
		MinParticipants: ptr(int32(6)),
		MaxParticipants: ptr(int32(30)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(10)),
		PublishState:    "published",
		OriginalID:      ptr(editableGame),
		CreatedAt:       time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC),
		Authors:         []string{testSessionUserID},
	})
}

// patchGame returns the response untouched, for the tests asserting on a
// refusal.
func patchGame(t *testing.T, baseURL, id, body string) (int, []byte) {
	t.Helper()

	res, err := authedPatch(t, baseURL+"/v1/games/"+id, body)
	if err != nil {
		t.Fatalf("PATCH /v1/games/%s: %v", id, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("could not read the response body: %v", err)
	}

	return res.StatusCode, raw
}

// editGame patches a game expecting success and decodes the result.
func editGame(t *testing.T, baseURL, id, body string) gameDetail {
	t.Helper()

	status, raw := patchGame(t, baseURL, id, body)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, raw)
	}

	var got gameDetail
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	return got
}

// TestHandleEditGameSetsTheFieldsItIsGiven is the third state, and the ordinary
// one: a value replaces what the column held.
func TestHandleEditGameSetsTheFieldsItIsGiven(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedEditFixture(t, ctx, pool)

	got := editGame(t, srv.URL, editableGame, `{
		"title": "Jogo do Lenço",
		"description": "Two teams, one handkerchief, plenty of running",
		"image": "https://example.test/lenco-2.png",
		"min_participants": 8,
		"max_participants": 24,
		"duration_min": 20,
		"duration_max": 35,
		"no_materials": true
	}`)

	if got.Description != "Two teams, one handkerchief, plenty of running" {
		t.Errorf("description = %q, want the new one", got.Description)
	}
	if !got.NoMaterials {
		t.Errorf("no_materials = false, want true")
	}

	assertStringPtr(t, "image", got.Image, ptr("https://example.test/lenco-2.png"))
	assertInt32Ptr(t, "min_participants", got.MinParticipants, ptr(int32(8)))
	assertInt32Ptr(t, "max_participants", got.MaxParticipants, ptr(int32(24)))
	assertInt32Ptr(t, "duration_min", got.DurationMin, ptr(int32(20)))
	assertInt32Ptr(t, "duration_max", got.DurationMax, ptr(int32(35)))
}

// TestHandleEditGameLeavesAbsentFieldsAlone is the first state. The body names
// one field, so every other column must come back exactly as the fixture wrote
// it — including the ones a handler that rebuilt the row from a zeroed struct
// would have quietly emptied.
func TestHandleEditGameLeavesAbsentFieldsAlone(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedEditFixture(t, ctx, pool)

	got := editGame(t, srv.URL, editableGame, `{"title": "Jogo do Lenço, revisto"}`)

	if got.Title != "Jogo do Lenço, revisto" {
		t.Errorf("title = %q, want the new one", got.Title)
	}
	if got.Description != "Two teams, one handkerchief" {
		t.Errorf("description = %q, want the fixture's", got.Description)
	}
	if got.NoMaterials {
		t.Errorf("no_materials = true, want the fixture's false")
	}

	assertStringPtr(t, "image", got.Image, ptr("https://example.test/lenco.png"))
	assertInt32Ptr(t, "min_participants", got.MinParticipants, ptr(int32(6)))
	assertInt32Ptr(t, "max_participants", got.MaxParticipants, ptr(int32(30)))
	assertInt32Ptr(t, "duration_min", got.DurationMin, ptr(int32(15)))
	assertInt32Ptr(t, "duration_max", got.DurationMax, ptr(int32(40)))
}

// TestHandleEditGameClearsImageOnNull is the second state on the one nullable
// column a non-variant game may empty freely.
func TestHandleEditGameClearsImageOnNull(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedEditFixture(t, ctx, pool)

	got := editGame(t, srv.URL, editableGame, `{"image": null}`)

	assertStringPtr(t, "image", got.Image, nil)

	// And nothing else moved: clearing one column must not be a rewrite of the
	// row around it.
	assertInt32Ptr(t, "min_participants", got.MinParticipants, ptr(int32(6)))
	if got.Title != "Jogo do Lenço" {
		t.Errorf("title = %q, want the fixture's", got.Title)
	}
}

// TestHandleEditGameClearsTheSpineOnNull is the second state on the four
// columns that carry it, one field at a time. A variant is the subject because
// it is the only kind of game the schema lets stand without them.
func TestHandleEditGameClearsTheSpineOnNull(t *testing.T) {
	fields := []struct {
		name string
		read func(gameDetail) *int32
	}{
		{"min_participants", func(g gameDetail) *int32 { return g.MinParticipants }},
		{"max_participants", func(g gameDetail) *int32 { return g.MaxParticipants }},
		{"duration_min", func(g gameDetail) *int32 { return g.DurationMin }},
		{"duration_max", func(g gameDetail) *int32 { return g.DurationMax }},
	}

	for _, f := range fields {
		t.Run(f.name, func(t *testing.T) {
			srv, pool := newAuthedTestServer(t)
			ctx := testContext(t)

			seedEditFixture(t, ctx, pool)
			seedEditableVariant(t, ctx, pool)

			got := editGame(t, srv.URL, testGameVariant, `{"`+f.name+`": null}`)

			assertInt32Ptr(t, f.name, f.read(got), nil)
		})
	}
}

// TestHandleEditGameSetsAndClearsAreDistinguishable puts the three states side
// by side on one field, which is the assertion the whole design exists to make
// possible: the same column reached through three bodies lands in three places.
func TestHandleEditGameSetsAndClearsAreDistinguishable(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedEditFixture(t, ctx, pool)

	set := editGame(t, srv.URL, editableGame, `{"image": "https://example.test/a.png"}`)
	assertStringPtr(t, "image after a value", set.Image, ptr("https://example.test/a.png"))

	absent := editGame(t, srv.URL, editableGame, `{"title": "untouched image"}`)
	assertStringPtr(t, "image after an absent field", absent.Image,
		ptr("https://example.test/a.png"))

	cleared := editGame(t, srv.URL, editableGame, `{"image": null}`)
	assertStringPtr(t, "image after a null", cleared.Image, nil)
}

// TestHandleEditGameRefusesToEmptyTheSpineOfANonVariant is the other side of
// the clearing tests. The same body that a variant accepts is a 422 here, and
// the message names the rule rather than the column.
func TestHandleEditGameRefusesToEmptyTheSpineOfANonVariant(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedEditFixture(t, ctx, pool)

	status, raw := patchGame(t, srv.URL, editableGame, `{"min_participants": null}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %s)",
			status, http.StatusUnprocessableEntity, raw)
	}

	assertErrorMessage(t, raw,
		"participants and duration are required unless the game is a variant")
}

// TestHandleEditGameRefusesANullOnANotNullColumn covers the three columns where
// null is not a meaning the schema has. They are caught in Go because a NOT
// NULL violation arrives without a constraint name, so the generic 422 it would
// otherwise produce would not tell the caller which field they emptied.
func TestHandleEditGameRefusesANullOnANotNullColumn(t *testing.T) {
	cases := []struct {
		field string
		want  string
	}{
		{"title", "title must not be null"},
		{"description", "description must not be null"},
		{"no_materials", "no_materials must not be null"},
	}

	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			srv, pool := newAuthedTestServer(t)
			ctx := testContext(t)

			seedEditFixture(t, ctx, pool)

			status, raw := patchGame(t, srv.URL, editableGame, `{"`+tc.field+`": null}`)
			if status != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want %d (body %s)",
					status, http.StatusUnprocessableEntity, raw)
			}

			assertErrorMessage(t, raw, tc.want)
		})
	}
}

// TestHandleEditGameRefusesAnEmptyTitle keeps whitespace from passing as a
// title, matching the create.
func TestHandleEditGameRefusesAnEmptyTitle(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedEditFixture(t, ctx, pool)

	status, raw := patchGame(t, srv.URL, editableGame, `{"title": "   "}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %s)",
			status, http.StatusUnprocessableEntity, raw)
	}

	assertErrorMessage(t, raw, "title must not be empty")
}

// TestHandleEditGameRefusesToBeToldTheStateOrTheAuthor is the create's rule
// restated for the edit, and it matters more here: publishing is a transition
// with preconditions of its own, and a PATCH that could write publish_state
// directly would be a way around every one of them.
func TestHandleEditGameRefusesToBeToldTheStateOrTheAuthor(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"publish_state", `{"publish_state": "published"}`},
		{"author_id", `{"author_id": "` + testAuthorID + `"}`},
		{"original_id", `{"original_id": "` + testGameVariant + `"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, pool := newAuthedTestServer(t)
			ctx := testContext(t)

			seedEditFixture(t, ctx, pool)

			status, raw := patchGame(t, srv.URL, editableGame, tc.body)
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want %d (body %s)",
					status, http.StatusBadRequest, raw)
			}

			var state string
			err := pool.QueryRow(ctx,
				`SELECT publish_state FROM games WHERE id = $1`, editableGame).Scan(&state)
			if err != nil {
				t.Fatalf("could not read the publish state: %v", err)
			}
			if state != "published" {
				t.Errorf("publish_state = %q, want it untouched at %q", state, "published")
			}
		})
	}
}

// TestHandleEditGameWithAnEmptyBodyChangesNothing pins the degenerate case. A
// body naming no field is a request for no change, and the point of the
// assertion is updated_at: an UPDATE run anyway would fire the trigger and
// record an edit that never happened.
func TestHandleEditGameWithAnEmptyBodyChangesNothing(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedEditFixture(t, ctx, pool)

	before := updatedAt(t, ctx, pool, editableGame)

	got := editGame(t, srv.URL, editableGame, `{}`)
	if got.Title != "Jogo do Lenço" {
		t.Errorf("title = %q, want the fixture's", got.Title)
	}

	if after := updatedAt(t, ctx, pool, editableGame); !after.Equal(before) {
		t.Errorf("updated_at moved from %s to %s on a body that named no field",
			before, after)
	}
}

// TestHandleEditGameTouchesUpdatedAt is the same assertion from the other side,
// so the test above cannot pass because the trigger is broken.
func TestHandleEditGameTouchesUpdatedAt(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedEditFixture(t, ctx, pool)

	before := updatedAt(t, ctx, pool, editableGame)

	editGame(t, srv.URL, editableGame, `{"title": "Jogo do Lenço, outra vez"}`)

	if after := updatedAt(t, ctx, pool, editableGame); !after.After(before) {
		t.Errorf("updated_at = %s, want it later than %s", after, before)
	}
}

// TestHandleEditGameHidesWhatTheCallerCannotSee reaches the edit through the
// same visibility rule as the reads: another author's draft is a 404 here too,
// since a refusal that distinguished "not yours" from "no such game" would
// confirm the row exists.
func TestHandleEditGameHidesWhatTheCallerCannotSee(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)
	insertGame(t, ctx, pool, testGame{
		ID:              testGameDraft,
		Title:           "Another author's draft",
		MinParticipants: ptr(int32(3)),
		MaxParticipants: ptr(int32(6)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(10)),
		PublishState:    "draft",
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	})

	status, raw := patchGame(t, srv.URL, testGameDraft, `{"title": "Mine now"}`)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusNotFound, raw)
	}

	var title string
	err := pool.QueryRow(ctx, `SELECT title FROM games WHERE id = $1`, testGameDraft).
		Scan(&title)
	if err != nil {
		t.Fatalf("could not read the title back: %v", err)
	}
	if title != "Another author's draft" {
		t.Errorf("title = %q, want it untouched", title)
	}
}

// TestHandleEditGameRejectsAMissingOrMalformedID separates the two shapes of
// bad id: one is a well-formed uuid naming nothing, the other is not a uuid.
func TestHandleEditGameRejectsAMissingOrMalformedID(t *testing.T) {
	cases := []struct {
		name string
		id   string
		want int
	}{
		{"missing", testGameMissing, http.StatusNotFound},
		{"malformed", "not-a-uuid", http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newAuthedTestServer(t)

			status, raw := patchGame(t, srv.URL, tc.id, `{"title": "Nowhere"}`)
			if status != tc.want {
				t.Errorf("status = %d, want %d (body %s)", status, tc.want, raw)
			}
		})
	}
}

// TestHandleEditGameNeedsASession guards the route, as the create's twin does.
func TestHandleEditGameNeedsASession(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedEditFixture(t, ctx, pool)

	req, err := http.NewRequest(http.MethodPatch,
		srv.URL+"/v1/games/"+editableGame, strings.NewReader(`{"title": "Mine now"}`))
	if err != nil {
		t.Fatalf("could not build the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH /v1/games/%s: %v", editableGame, err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", res.StatusCode, http.StatusUnauthorized)
	}
}

// updatedAt reads the column the trigger maintains, which is the only way to
// tell a no-op apart from an update that wrote the same values back.
func updatedAt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string) time.Time {
	t.Helper()

	var at time.Time
	if err := pool.QueryRow(ctx,
		`SELECT updated_at FROM games WHERE id = $1`, id).Scan(&at); err != nil {
		t.Fatalf("could not read updated_at for game %s: %v", id, err)
	}

	return at
}
