package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Vidal322/activity-library/internal/store"
)

// POST /v1/games/{id}/publish and its /unpublish twin move a game between the
// only two publish_state values there are. There is no third: unpublishing
// returns a game to 'draft', the state it was created in.
//
// publish_state is only half of the visibility rule, since an author is shown
// their own drafts either way. So the test that watches the list does it from
// the stranger's side, which is the only side where the state decides anything.

// publishGameID is the row these tests move back and forth.
const publishGameID = testGamePublishedOlder

// seedPublishFixture writes one game in the given state, authored by the
// caller so that they may publish it. testAuthorID comes along because the
// round trip hands the credit to that account and back.
func seedPublishFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, state string) {
	t.Helper()

	seedDraft(t, ctx, pool, state, draftParts{
		description: "Two teams, one handkerchief",
		category:    true,
		block:       true,
	})
}

type draftParts struct {
	description string
	category    bool
	block       bool
}

func seedDraft(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	state string,
	parts draftParts,
) {
	t.Helper()

	insertAuthor(t, ctx, pool, testAuthorID)

	insertGame(t, ctx, pool, testGame{
		ID:              publishGameID,
		Title:           "Jogo do Lenço",
		Description:     parts.description,
		MinParticipants: ptr(int32(6)),
		MaxParticipants: ptr(int32(30)),
		DurationMin:     ptr(int32(15)),
		DurationMax:     ptr(int32(40)),
		PublishState:    state,
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Authors:         []string{testSessionUserID},
	})

	if parts.category {
		insertCategory(t, ctx, pool, gameCategory{
			ID:           testCategoryIcebreaker,
			Name:         "Icebreaker",
			Description:  "Opens a group that has just met.",
			Active:       true,
			DisplayOrder: 1,
			FamilyID:     testFamilyPurpose,
			FamilyName:   "Purpose",
		}, 1)
		linkGameCategory(t, ctx, pool, publishGameID, testCategoryIcebreaker)
	}

	if parts.block {
		insertBlock(t, ctx, pool, publishGameID, gameBlock{
			ID:       testBlockSteps,
			Type:     "steps",
			Content:  "Split into two teams and line them up.",
			Position: 0,
		})
	}
}

// postPublish returns the response untouched, for the tests asserting on a
// refusal. The transition is the last path segment, "publish" or "unpublish",
// so a test can send both down one helper.
func postPublish(t *testing.T, baseURL, id, action string) (int, []byte) {
	t.Helper()

	res, err := authedPost(t, baseURL+"/v1/games/"+id+"/"+action, "")
	if err != nil {
		t.Fatalf("POST /v1/games/%s/%s: %v", id, action, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("could not read the response body: %v", err)
	}

	return res.StatusCode, raw
}

// setPublished performs the transition expecting success and decodes the game.
func setPublished(t *testing.T, baseURL, id, action string) gameDetail {
	t.Helper()

	status, raw := postPublish(t, baseURL, id, action)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, raw)
	}

	var got gameDetail
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	return got
}

// publishStateOf reads the column straight out of the table, past the handler
// and past visibility, so a test can assert on a row it is not allowed to
// fetch.
func publishStateOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string) string {
	t.Helper()

	var state string
	err := pool.QueryRow(ctx, `SELECT publish_state FROM games WHERE id = $1`, id).Scan(&state)
	if err != nil {
		t.Fatalf("could not read the publish state of game %s: %v", id, err)
	}

	return state
}

// assertPublishState checks the response and the row together. The endpoint
// answers with the game it just wrote, so a handler that reported the state it
// meant to set rather than the one it stored would pass on the body alone.
func assertPublishState(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	got gameDetail,
	want string,
) {
	t.Helper()

	if got.PublishState != want {
		t.Errorf("publish_state = %q, want %q", got.PublishState, want)
	}
	if stored := publishStateOf(t, ctx, pool, got.ID); stored != want {
		t.Errorf("games.publish_state = %q, want %q", stored, want)
	}
}

func TestHandlePublishGamePublishesTheCallersDraft(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedPublishFixture(t, ctx, pool, "draft")

	got := setPublished(t, srv.URL, publishGameID, "publish")

	assertPublishState(t, ctx, pool, got, "published")
}

// TestHandleUnpublishGameReturnsTheGameToDraft pins the state it lands in.
// 'draft' is the whole of the other half of the CHECK constraint, and a value
// invented for this endpoint would be rejected by the table.
func TestHandleUnpublishGameReturnsTheGameToDraft(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedPublishFixture(t, ctx, pool, "published")

	got := setPublished(t, srv.URL, publishGameID, "unpublish")

	assertPublishState(t, ctx, pool, got, "draft")
}

// TestPublishRoundTripAddsAndRemovesTheGameFromTheList is what the issue asks
// for: publish a game, see it in the list, unpublish it, see it gone.
//
// The caller has to be the author to write, and must not be to observe, since
// their own drafts are listed for them either way. So the credit moves to
// another account for each reading and comes straight back: write as the
// author, read as everyone else.
func TestPublishRoundTripAddsAndRemovesTheGameFromTheList(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedPublishFixture(t, ctx, pool, "draft")

	asStranger := func() gamesListResponse {
		reassignAuthor(t, ctx, pool, publishGameID, testSessionUserID, testAuthorID)
		defer reassignAuthor(t, ctx, pool, publishGameID, testAuthorID, testSessionUserID)

		return getGames(t, srv.URL)
	}

	// The draft starts hidden, so the publish below is what puts it in the
	// list rather than the fixture having been visible all along.
	assertGameIDs(t, asStranger())

	setPublished(t, srv.URL, publishGameID, "publish")
	assertGameIDs(t, asStranger(), publishGameID)

	setPublished(t, srv.URL, publishGameID, "unpublish")
	assertGameIDs(t, asStranger())
}

// TestHandlePublishGameIsANoOpWhenAlreadyInThatState is the issue's other
// promise: publishing an already-published game answers 200, not a conflict.
// Both directions are idempotent, so both are checked.
func TestHandlePublishGameIsANoOpWhenAlreadyInThatState(t *testing.T) {
	cases := []struct {
		action string
		state  string
	}{
		{"publish", "published"},
		{"unpublish", "draft"},
	}

	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			srv, pool := newAuthedTestServer(t)
			ctx := testContext(t)

			seedPublishFixture(t, ctx, pool, tc.state)

			got := setPublished(t, srv.URL, publishGameID, tc.action)

			assertPublishState(t, ctx, pool, got, tc.state)
		})
	}
}

// TestHandlePublishGameRejectsANonAuthor covers the "only the author may" half
// on a game the caller can see: the row is published, so a 403 gives away
// nothing the list had not already shown them.
func TestHandlePublishGameRejectsANonAuthor(t *testing.T) {
	for _, action := range []string{"publish", "unpublish"} {
		t.Run(action, func(t *testing.T) {
			srv, pool := newAuthedTestServer(t)
			ctx := testContext(t)

			seedSomeoneElsesGame(t, ctx, pool)

			status, raw := postPublish(t, srv.URL, testGamePublishedNewer, action)
			if status != http.StatusForbidden {
				t.Fatalf("status = %d, want %d (body %s)",
					status, http.StatusForbidden, raw)
			}

			stored := publishStateOf(t, ctx, pool, testGamePublishedNewer)
			if stored != "published" {
				t.Errorf("games.publish_state = %q, want it left at %q", stored, "published")
			}
		})
	}
}

// TestHandlePublishGameHidesAnotherAuthorsDraft is the same refusal one step
// further in, and it is a 404 rather than a 403 for the reason the detail
// endpoint is: whether an unpublished game exists is not a stranger's to learn.
func TestHandlePublishGameHidesAnotherAuthorsDraft(t *testing.T) {
	for _, action := range []string{"publish", "unpublish"} {
		t.Run(action, func(t *testing.T) {
			srv, pool := newAuthedTestServer(t)
			ctx := testContext(t)

			insertAuthor(t, ctx, pool, testAuthorID)
			insertGame(t, ctx, pool, testGame{
				ID:              testGamePublishedNewer,
				Title:           "Another author's draft",
				Description:     "not mine, and unpublished",
				MinParticipants: ptr(int32(3)),
				MaxParticipants: ptr(int32(6)),
				DurationMin:     ptr(int32(5)),
				DurationMax:     ptr(int32(10)),
				PublishState:    "draft",
				CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
				Authors:         []string{testAuthorID},
			})

			status, raw := postPublish(t, srv.URL, testGamePublishedNewer, action)
			if status != http.StatusNotFound {
				t.Fatalf("status = %d, want %d (body %s)",
					status, http.StatusNotFound, raw)
			}

			stored := publishStateOf(t, ctx, pool, testGamePublishedNewer)
			if stored != "draft" {
				t.Errorf("games.publish_state = %q, want it left at %q", stored, "draft")
			}
		})
	}
}

func TestHandlePublishGameRejectsAMissingGame(t *testing.T) {
	for _, action := range []string{"publish", "unpublish"} {
		t.Run(action, func(t *testing.T) {
			srv, _ := newAuthedTestServer(t)

			status, raw := postPublish(t, srv.URL, testGameMissing, action)
			if status != http.StatusNotFound {
				t.Fatalf("status = %d, want %d (body %s)",
					status, http.StatusNotFound, raw)
			}
		})
	}
}

func TestHandlePublishGameNeedsASession(t *testing.T) {
	for _, action := range []string{"publish", "unpublish"} {
		t.Run(action, func(t *testing.T) {
			srv, pool := newAuthedTestServer(t)
			ctx := testContext(t)

			seedPublishFixture(t, ctx, pool, "draft")

			req, err := http.NewRequest(http.MethodPost,
				srv.URL+"/v1/games/"+publishGameID+"/"+action, nil)
			if err != nil {
				t.Fatalf("could not build the request: %v", err)
			}

			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("POST without a session: %v", err)
			}
			defer res.Body.Close()

			if res.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusUnauthorized)
			}

			if stored := publishStateOf(t, ctx, pool, publishGameID); stored != "draft" {
				t.Errorf("games.publish_state = %q, want it left at %q", stored, "draft")
			}
		})
	}
}

// TestSetPublishStateRefusesANonAuthor reaches past the handler on purpose,
// the way the other store tests do. The handler checks authorship before it
// calls in, so without this the check inside the transaction could be deleted
// and every test above would still pass.
func TestSetPublishStateRefusesANonAuthor(t *testing.T) {
	_, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedSomeoneElsesGame(t, ctx, pool)

	_, err := store.PublishGame(ctx, pool, testGamePublishedNewer, testSessionUserID)
	if !errors.Is(err, store.ErrForbidden) {
		t.Errorf("PublishGame() error = %v, want %v", err, store.ErrForbidden)
	}

	_, err = store.UnpublishGame(ctx, pool, testGamePublishedNewer, testSessionUserID)
	if !errors.Is(err, store.ErrForbidden) {
		t.Errorf("UnpublishGame() error = %v, want %v", err, store.ErrForbidden)
	}

	if stored := publishStateOf(t, ctx, pool, testGamePublishedNewer); stored != "published" {
		t.Errorf("games.publish_state = %q, want it left at %q", stored, "published")
	}
}

func refusePublish(t *testing.T, baseURL, id string) []string {
	t.Helper()

	status, raw := postPublish(t, baseURL, id, "publish")
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %s)",
			status, http.StatusUnprocessableEntity, raw)
	}

	var got struct {
		Error   string   `json:"error"`
		Details []string `json:"details"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	if got.Error != msgIncompleteDraft {
		t.Errorf("error = %q, want %q", got.Error, msgIncompleteDraft)
	}

	return got.Details
}

func TestHandlePublishGameReportsEveryFailedPrecondition(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedDraft(t, ctx, pool, "draft", draftParts{})

	got := refusePublish(t, srv.URL, publishGameID)

	want := []string{
		"a published game needs at least one block",
		"a published game needs at least one category",
		"a published game needs a description",
	}
	if !slices.Equal(got, want) {
		t.Errorf("details = %q, want %q", got, want)
	}

	if stored := publishStateOf(t, ctx, pool, publishGameID); stored != "draft" {
		t.Errorf("games.publish_state = %q, want it left at %q", stored, "draft")
	}
}

func TestHandlePublishGameReportsEachPreconditionOnItsOwn(t *testing.T) {
	const description = "Two teams, one handkerchief"

	cases := []struct {
		name  string
		parts draftParts
		want  string
	}{
		{
			name:  "no blocks",
			parts: draftParts{description: description, category: true},
			want:  "a published game needs at least one block",
		},
		{
			name:  "no categories",
			parts: draftParts{description: description, block: true},
			want:  "a published game needs at least one category",
		},
		{
			name:  "no description",
			parts: draftParts{description: "   ", category: true, block: true},
			want:  "a published game needs a description",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, pool := newAuthedTestServer(t)
			ctx := testContext(t)

			seedDraft(t, ctx, pool, "draft", tc.parts)

			got := refusePublish(t, srv.URL, publishGameID)

			if !slices.Equal(got, []string{tc.want}) {
				t.Errorf("details = %q, want only %q", got, tc.want)
			}

			if stored := publishStateOf(t, ctx, pool, publishGameID); stored != "draft" {
				t.Errorf("games.publish_state = %q, want it left at %q", stored, "draft")
			}
		})
	}
}

func TestHandleUnpublishGameIgnoresThePreconditions(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedDraft(t, ctx, pool, "published", draftParts{})

	got := setPublished(t, srv.URL, publishGameID, "unpublish")

	assertPublishState(t, ctx, pool, got, "draft")
}
