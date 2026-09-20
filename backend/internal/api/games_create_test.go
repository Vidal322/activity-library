package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Creating a game is the first point where the API writes on someone's behalf,
// and two of its facts are not the caller's to state: who wrote the game, and
// whether it is published. Both are decided by the server, so the tests below
// spend most of their effort on bodies that try to state them anyway.
//
// The caller throughout is the account seedSession creates, which is why these
// tests expect testSessionUserID in the credit line rather than the
// testAuthorID the read fixtures hand out.

// completeDraftBody is the smallest body the schema accepts. The four numbers
// are not optional padding: games_non_variant_complete requires both
// participant counts and both durations on any game that is not a variant, so
// a draft starts with the title plus that spine and fills in everything else
// later.
const completeDraftBody = `{
	"title": "Corrida dos Sacos",
	"description": "",
	"min_participants": 4,
	"max_participants": 20,
	"duration_min": 10,
	"duration_max": 25
}`

// postGame performs the create and returns the response untouched, so a test
// can assert on a status the decoding helper would have failed on.
func postGame(t *testing.T, baseURL, body string) (int, []byte, string) {
	t.Helper()

	res, err := authedPost(t, baseURL+"/v1/games", body)
	if err != nil {
		t.Fatalf("POST /v1/games: %v", err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("could not read the response body: %v", err)
	}

	return res.StatusCode, raw, res.Header.Get("Location")
}

// createDraft posts a body that is expected to succeed and hands back the
// decoded game, so the tests that care about what happened next do not repeat
// the status check.
func createDraft(t *testing.T, baseURL, body string) gameDetail {
	t.Helper()

	status, raw, _ := postGame(t, baseURL, body)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusCreated, raw)
	}

	var got gameDetail
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	return got
}

// countGames is how the refusal tests prove nothing was written. A 400 that
// still left a row behind would pass every assertion on the response alone.
func countGames(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM games`).Scan(&n); err != nil {
		t.Fatalf("could not count the games: %v", err)
	}

	return n
}

// assertErrorMessage checks the message a refusal carries, not merely that one
// is present: these endpoints have several ways to reach the same status, and
// the message is what tells the caller which rule they broke.
func assertErrorMessage(t *testing.T, raw []byte, want string) {
	t.Helper()

	var body map[string]string
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("could not decode the error body %s: %v", raw, err)
	}

	if body["error"] != want {
		t.Errorf("error = %q, want %q", body["error"], want)
	}
}

// TestHandleCreateDraftReturnsTheDraft pins the response shape: the full game,
// not just its id, so the editor the caller lands in has everything it needs
// without a second request.
func TestHandleCreateDraftReturnsTheDraft(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	got := createDraft(t, srv.URL, completeDraftBody)

	if got.ID == "" {
		t.Errorf("id = %q, want the id the database generated", got.ID)
	}
	if got.Title != "Corrida dos Sacos" {
		t.Errorf("title = %q, want %q", got.Title, "Corrida dos Sacos")
	}

	assertInt32Ptr(t, "min_participants", got.MinParticipants, ptr(int32(4)))
	assertInt32Ptr(t, "max_participants", got.MaxParticipants, ptr(int32(20)))
	assertInt32Ptr(t, "duration_min", got.DurationMin, ptr(int32(10)))
	assertInt32Ptr(t, "duration_max", got.DurationMax, ptr(int32(25)))

	// A game this new has nothing hanging off it, and the client should not
	// have to tell an empty list from a null one.
	if got.Categories == nil || got.Locations == nil ||
		got.Materials == nil || got.Blocks == nil {
		t.Errorf("categories/locations/materials/blocks = %v/%v/%v/%v, want empty arrays",
			got.Categories, got.Locations, got.Materials, got.Blocks)
	}
}

// TestHandleCreateDraftCreditsTheSessionUser is the first half of the rule the
// endpoint exists to enforce: the author is whoever is holding the cookie.
func TestHandleCreateDraftCreditsTheSessionUser(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	// A second account the body never names, so a handler that credited the
	// first user it found rather than the session would have somewhere to go
	// wrong.
	insertAuthor(t, ctx, pool, testAuthorID)

	got := createDraft(t, srv.URL, completeDraftBody)

	assertAuthors(t, got.Authors, []gameAuthor{{ID: testSessionUserID, Name: "Session User"}})

	var authorID string
	err := pool.QueryRow(ctx,
		`SELECT user_id FROM game_authors WHERE game_id = $1`, got.ID).Scan(&authorID)
	if err != nil {
		t.Fatalf("could not read the credit for game %s: %v", got.ID, err)
	}
	if authorID != testSessionUserID {
		t.Errorf("game_authors.user_id = %s, want %s", authorID, testSessionUserID)
	}
}

// TestHandleCreateDraftStartsUnpublished is the second half: a new game is a
// draft, which is why it is absent from the list a stranger reads even though
// the caller can see it themselves.
func TestHandleCreateDraftStartsUnpublished(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	got := createDraft(t, srv.URL, completeDraftBody)

	if got.PublishState != "draft" {
		t.Errorf("publish_state = %q, want %q", got.PublishState, "draft")
	}

	var state string
	err := pool.QueryRow(ctx,
		`SELECT publish_state FROM games WHERE id = $1`, got.ID).Scan(&state)
	if err != nil {
		t.Fatalf("could not read the publish state of game %s: %v", got.ID, err)
	}
	if state != "draft" {
		t.Errorf("games.publish_state = %q, want %q", state, "draft")
	}
}

// TestHandleCreateDraftRefusesToBeToldTheAuthorOrTheState covers the body that
// tries to decide either. Neither field exists on the request type and readJSON
// rejects unknown fields, so the attempt is refused rather than ignored: a
// caller who thought they were setting the author should not be told they
// succeeded.
func TestHandleCreateDraftRefusesToBeToldTheAuthorOrTheState(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "author_id",
			body: `{"title": "Mine now", "min_participants": 4, "max_participants": 20,
			        "duration_min": 10, "duration_max": 25,
			        "author_id": "` + testAuthorID + `"}`,
		},
		{
			name: "authors",
			body: `{"title": "Mine now", "min_participants": 4, "max_participants": 20,
			        "duration_min": 10, "duration_max": 25,
			        "authors": [{"id": "` + testAuthorID + `"}]}`,
		},
		{
			name: "publish_state",
			body: `{"title": "Published at birth", "min_participants": 4, "max_participants": 20,
			        "duration_min": 10, "duration_max": 25,
			        "publish_state": "published"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, pool := newAuthedTestServer(t)
			ctx := testContext(t)

			status, raw, _ := postGame(t, srv.URL, tc.body)
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want %d (body %s)",
					status, http.StatusBadRequest, raw)
			}

			if n := countGames(t, ctx, pool); n != 0 {
				t.Errorf("games table holds %d rows, want 0: the refused body was written anyway", n)
			}
		})
	}
}

// TestHandleCreateDraftNeedsASession guards the route itself. The middleware is
// what supplies the author, so an unauthenticated create has nobody to credit
// and must not reach the store.
func TestHandleCreateDraftNeedsASession(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	res, err := http.Post(srv.URL+"/v1/games", "application/json",
		strings.NewReader(completeDraftBody))
	if err != nil {
		t.Fatalf("POST /v1/games: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", res.StatusCode, http.StatusUnauthorized)
	}

	if n := countGames(t, ctx, pool); n != 0 {
		t.Errorf("games table holds %d rows, want 0", n)
	}
}

// TestHandleCreateDraftRefusesAnIncompleteGame is the boundary the issue draws.
// A draft may arrive without a description, a category or a single block, but
// not without the participant and duration spine: that is the one rule
// games_non_variant_complete holds a non-variant game to from the start.
func TestHandleCreateDraftRefusesAnIncompleteGame(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	status, raw, _ := postGame(t, srv.URL, `{"title": "Little more than a title"}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %s)",
			status, http.StatusUnprocessableEntity, raw)
	}

	assertErrorMessage(t, raw,
		"participants and duration are required unless the game is a variant")
}

// TestHandleCreateDraftRefusesAnEmptyTitle keeps the one field a game cannot
// start without. Whitespace counts as empty, since the handler trims before it
// looks.
func TestHandleCreateDraftRefusesAnEmptyTitle(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	body := `{"title": "   ", "min_participants": 4, "max_participants": 20,
	          "duration_min": 10, "duration_max": 25}`

	status, raw, _ := postGame(t, srv.URL, body)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %s)",
			status, http.StatusUnprocessableEntity, raw)
	}

	assertErrorMessage(t, raw, "title is required")
}

// TestHandleCreateDraftLocationResolves follows the header the 201 carries. It
// resolving to the game is what makes the response more than a claim, and it
// only works because the caller is the author: the same request from anyone
// else would be a 404.
func TestHandleCreateDraftLocationResolves(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	status, raw, location := postGame(t, srv.URL, completeDraftBody)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusCreated, raw)
	}

	res, err := authedGet(t, srv.URL+location)
	if err != nil {
		t.Fatalf("GET %s: %v", location, err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d", location, res.StatusCode, http.StatusOK)
	}

	var got gameDetail
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	if location != apiPrefix+"/games/"+got.ID {
		t.Errorf("Location = %q, want %q", location, apiPrefix+"/games/"+got.ID)
	}
}

// TestHandleCreateDraftAppearsInTheAuthorsList joins this endpoint to the
// visibility rule next door: the game the caller just wrote is unpublished, and
// listing it anyway is the whole point of showing authors their own drafts.
func TestHandleCreateDraftAppearsInTheAuthorsList(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	got := createDraft(t, srv.URL, completeDraftBody)

	assertGameIDs(t, getGames(t, srv.URL), got.ID)
}
