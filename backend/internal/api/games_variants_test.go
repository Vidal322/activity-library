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

// POST /v1/games/{id}/variants writes a game derived from another one. The
// derivation is a single column, original_id, and every rule that follows from
// it is already in the schema: is_variant is generated from it, the composite
// foreign key keeps a variant from being derived from a variant, and
// games_non_variant_complete stops demanding participants and duration once it
// is set. So these tests are mostly about which of those rules reach the caller
// as what, and about the one thing the schema cannot decide: whose game the new
// draft is.
//
// The original below is authored by testAuthorID and published, so the caller
// is a stranger to it. That is the ordinary case: variants exist so that
// someone can adapt a game they found, not only one they wrote.

// variantOriginalID is the game these tests derive from.
const variantOriginalID = testGamePublishedNewer

// msgOriginalRefused is what games_original_not_variant_fkey reads as when an
// insert names an original it does not admit.
const msgOriginalRefused = "the original game does not exist, is itself a variant, or is not published"

// seedOriginal writes the game the variants hang off, authored by somebody
// other than the caller.
func seedOriginal(t *testing.T, ctx context.Context, pool *pgxpool.Pool, state string) {
	t.Helper()

	insertAuthor(t, ctx, pool, testAuthorID)

	insertGame(t, ctx, pool, testGame{
		ID:              variantOriginalID,
		Title:           "Jogo do Lenco",
		Description:     "Two teams, one handkerchief",
		MinParticipants: ptr(int32(6)),
		MaxParticipants: ptr(int32(30)),
		DurationMin:     ptr(int32(15)),
		DurationMax:     ptr(int32(40)),
		PublishState:    state,
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	})
}

// variantBody is the smallest body this endpoint accepts, and the reason the
// endpoint is not simply POST /v1/games: a variant may leave the participant
// and duration spine null, which a plain draft may not.
const variantBody = `{"title": "Jogo do Lenco, versao de sala"}`

func postVariant(t *testing.T, baseURL, gameID, body string) (int, []byte, string) {
	t.Helper()

	url := baseURL + "/v1/games/" + gameID + "/variants"

	res, err := authedPost(t, url, body)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("could not read the response body: %v", err)
	}

	return res.StatusCode, raw, res.Header.Get("Location")
}

// createVariant posts a body that is expected to succeed and hands back the
// decoded game.
func createVariant(t *testing.T, baseURL, gameID, body string) gameDetail {
	t.Helper()

	status, raw, _ := postVariant(t, baseURL, gameID, body)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusCreated, raw)
	}

	var got gameDetail
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	return got
}

// originalOf reads the column the endpoint exists to set. It is not on the wire
// yet, so the database is the only place to see it.
func originalOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, gameID string) *string {
	t.Helper()

	var originalID *string
	err := pool.QueryRow(ctx,
		`SELECT original_id::text FROM games WHERE id = $1`, gameID).Scan(&originalID)
	if err != nil {
		t.Fatalf("could not read original_id of game %s: %v", gameID, err)
	}

	return originalID
}

// TestHandleCreateVariantDerivesADraft is the endpoint's whole purpose: a new
// draft pointing at the original, credited to the caller rather than to the
// author of the game it came from, and holding none of the numbers the schema
// would have demanded of a game standing on its own.
func TestHandleCreateVariantDerivesADraft(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedOriginal(t, ctx, pool, "published")

	got := createVariant(t, srv.URL, variantOriginalID, variantBody)

	if got.ID == "" || got.ID == variantOriginalID {
		t.Errorf("id = %q, want an id of its own", got.ID)
	}
	if got.Title != "Jogo do Lenco, versao de sala" {
		t.Errorf("title = %q, want %q", got.Title, "Jogo do Lenco, versao de sala")
	}
	if got.PublishState != "draft" {
		t.Errorf("publish_state = %q, want %q", got.PublishState, "draft")
	}

	assertInt32Ptr(t, "own.min_participants", got.Own.MinParticipants, nil)
	assertInt32Ptr(t, "own.max_participants", got.Own.MaxParticipants, nil)
	assertInt32Ptr(t, "own.duration_min", got.Own.DurationMin, nil)
	assertInt32Ptr(t, "own.duration_max", got.Own.DurationMax, nil)

	// The variant is the caller's own game. Deriving from someone else's work
	// does not make them an author of the result.
	assertAuthors(t, got.Authors, []gameAuthor{{ID: testSessionUserID, Name: "Session User"}})

	originalID := originalOf(t, ctx, pool, got.ID)
	if originalID == nil || *originalID != variantOriginalID {
		t.Errorf("games.original_id = %v, want %s", deref(originalID), variantOriginalID)
	}

	// And the original is untouched: it is read, not adapted in place.
	if originalOf(t, ctx, pool, variantOriginalID) != nil {
		t.Errorf("the original now carries an original_id of its own")
	}
}

// TestHandleCreateVariantMayStateItsOwnSpine is the other half of the nullable
// columns: a variant that does shorten the game says so, and the numbers it
// gives are its own.
func TestHandleCreateVariantMayStateItsOwnSpine(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedOriginal(t, ctx, pool, "published")

	body := `{"title": "Versao relampago", "duration_min": 5, "duration_max": 10}`
	got := createVariant(t, srv.URL, variantOriginalID, body)

	assertInt32Ptr(t, "own.duration_min", got.Own.DurationMin, ptr(int32(5)))
	assertInt32Ptr(t, "own.duration_max", got.Own.DurationMax, ptr(int32(10)))
	assertInt32Ptr(t, "own.min_participants", got.Own.MinParticipants, nil)
	assertInt32Ptr(t, "own.max_participants", got.Own.MaxParticipants, nil)
}

// TestHandleCreateVariantRefusesASecondLevel is the rule the composite foreign
// key encodes: variants are one level deep, so a variant of a variant is a 422
// naming the reason rather than the 500 an unhandled constraint violation
// would be.
func TestHandleCreateVariantRefusesASecondLevel(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedOriginal(t, ctx, pool, "published")

	first := createVariant(t, srv.URL, variantOriginalID, variantBody)

	status, raw, _ := postVariant(t, srv.URL, first.ID,
		`{"title": "Uma variante da variante"}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %s)",
			status, http.StatusUnprocessableEntity, raw)
	}

	assertErrorMessage(t, raw, msgOriginalRefused)

	// Two games: the original and the one variant it is allowed.
	if n := countGames(t, ctx, pool); n != 2 {
		t.Errorf("games table holds %d rows, want 2: the refused variant was written anyway", n)
	}
}

// TestHandleCreateVariantRefusesADraftTheCallerWrote covers the one game the
// visibility check admits but the schema does not: the caller's own draft.
// They can see it, so this is no 404; games_original_not_variant_fkey only
// matches published originals, so the insert is refused with a 422.
func TestHandleCreateVariantRefusesADraftTheCallerWrote(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	insertGame(t, ctx, pool, testGame{
		ID:              testGameDraft,
		Title:           "Ainda por publicar",
		MinParticipants: ptr(int32(4)),
		MaxParticipants: ptr(int32(12)),
		DurationMin:     ptr(int32(10)),
		DurationMax:     ptr(int32(20)),
		PublishState:    "draft",
		CreatedAt:       time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC),
		Authors:         []string{testSessionUserID},
	})

	status, raw, _ := postVariant(t, srv.URL, testGameDraft, variantBody)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %s)",
			status, http.StatusUnprocessableEntity, raw)
	}

	assertErrorMessage(t, raw, msgOriginalRefused)

	if n := countGames(t, ctx, pool); n != 1 {
		t.Errorf("games table holds %d rows, want 1: the refused variant was written anyway", n)
	}
}

// TestHandleCreateVariantOfAGameTheCallerCannotSee is the same visibility rule
// from the other side. A stranger's draft answers 404 rather than 403: the
// caller is not meant to learn that the row exists, and the foreign key on its
// own would have let the variant through.
func TestHandleCreateVariantOfAGameTheCallerCannotSee(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedOriginal(t, ctx, pool, "draft")

	status, raw, _ := postVariant(t, srv.URL, variantOriginalID, variantBody)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusNotFound, raw)
	}

	if n := countGames(t, ctx, pool); n != 1 {
		t.Errorf("games table holds %d rows, want 1", n)
	}
}

// TestHandleCreateVariantOfAGameThatDoesNotExist is the missing original, which
// reaches the caller the same way a hidden one does.
func TestHandleCreateVariantOfAGameThatDoesNotExist(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	status, raw, _ := postVariant(t, srv.URL, testGameMissing, variantBody)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusNotFound, raw)
	}

	if n := countGames(t, ctx, pool); n != 0 {
		t.Errorf("games table holds %d rows, want 0", n)
	}
}

// TestHandleCreateVariantRejectsAMalformedID keeps a path that is not a uuid
// away from the database, where it would arrive as a cast error rather than as
// a bad request.
func TestHandleCreateVariantRejectsAMalformedID(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	status, raw, _ := postVariant(t, srv.URL, "not-a-uuid", variantBody)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusBadRequest, raw)
	}

	assertErrorMessage(t, raw, "malformed game id")
}

// TestHandleCreateVariantRequiresATitle is the one field the relaxed body still
// insists on: a variant may leave its numbers unsaid, but not its name.
func TestHandleCreateVariantRequiresATitle(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedOriginal(t, ctx, pool, "published")

	status, raw, _ := postVariant(t, srv.URL, variantOriginalID, `{"title": "   "}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %s)",
			status, http.StatusUnprocessableEntity, raw)
	}

	assertErrorMessage(t, raw, "title is required")

	if n := countGames(t, ctx, pool); n != 1 {
		t.Errorf("games table holds %d rows, want 1", n)
	}
}

// TestHandleCreateVariantNeedsASession guards the route: the session is what
// decides whose variant this is, so there is nobody to credit without one.
func TestHandleCreateVariantNeedsASession(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedOriginal(t, ctx, pool, "published")

	res, err := http.Post(srv.URL+"/v1/games/"+variantOriginalID+"/variants",
		"application/json", strings.NewReader(variantBody))
	if err != nil {
		t.Fatalf("POST /v1/games/{id}/variants: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", res.StatusCode, http.StatusUnauthorized)
	}

	if n := countGames(t, ctx, pool); n != 1 {
		t.Errorf("games table holds %d rows, want 1", n)
	}
}

// TestHandleCreateVariantLocationResolves follows the header the 201 carries:
// the caller authored the variant, so the draft it names is one they may read.
func TestHandleCreateVariantLocationResolves(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedOriginal(t, ctx, pool, "published")

	status, raw, location := postVariant(t, srv.URL, variantOriginalID, variantBody)
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
