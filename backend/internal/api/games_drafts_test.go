package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A draft belongs to the person writing it and to nobody else. These tests
// cover both halves of that: the caller sees their own unpublished work
// wherever published games appear, and another author's draft is not merely
// forbidden but absent — a stranger asking for one is told it does not exist,
// because whether it exists is not theirs to learn.
//
// The caller throughout is the account seedSession creates, which is why these
// tests credit testSessionUserID rather than the testAuthorID every other
// fixture uses.

// seedDraftFixture writes three rows around the one distinction that matters:
// who wrote the unpublished ones.
//
//	older   "Published by someone else"  published, testAuthorID
//	mine    "My own draft"               draft,     testSessionUserID
//	theirs  "Another author's draft"     draft,     testAuthorID
//
// Newest last, so a query that returns the drafts in the wrong order fails on
// position rather than on membership.
func seedDraftFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	insertAuthor(t, ctx, pool, testAuthorID)

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedOlder,
		Title:           "Published by someone else",
		Description:     "visible to everyone",
		MinParticipants: ptr(int32(4)),
		MaxParticipants: ptr(int32(12)),
		DurationMin:     ptr(int32(10)),
		DurationMax:     ptr(int32(20)),
		PublishState:    "published",
		CreatedAt:       base,
	})
	insertGame(t, ctx, pool, testGame{
		ID:              testGameDraft,
		Title:           "My own draft",
		Description:     "mine, and unpublished",
		MinParticipants: ptr(int32(3)),
		MaxParticipants: ptr(int32(6)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(10)),
		PublishState:    "draft",
		CreatedAt:       base.Add(time.Hour),
		Authors:         []string{testSessionUserID},
	})
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "Another author's draft",
		Description:     "not mine, and unpublished",
		MinParticipants: ptr(int32(3)),
		MaxParticipants: ptr(int32(6)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(10)),
		PublishState:    "draft",
		CreatedAt:       base.Add(2 * time.Hour),
	})
}

// TestHandleGamesListShowsTheCallerTheirOwnDraft is the point of the feature:
// an author's unpublished work is listed for them, in the same order as
// everything else, while the draft next to it that they did not write is not.
func TestHandleGamesListShowsTheCallerTheirOwnDraft(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedDraftFixture(t, ctx, pool)

	body := getGames(t, srv.URL)

	// Newest first. The other author's draft is newer than both and still
	// absent, so a query that leaked every draft would fail on the first id.
	assertGameIDs(t, body, testGameDraft, testGamePublishedOlder)
}

// TestHandleGamesListHidesDraftsFromEveryoneElse is the same table read by a
// caller who wrote none of it: the two drafts vanish and the published row is
// all that remains.
//
// The fixture credits testSessionUserID, so this test re-crediting that draft
// to another account is what separates "published only" from "mine only".
func TestHandleGamesListHidesDraftsFromEveryoneElse(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedDraftFixture(t, ctx, pool)
	reassignAuthor(t, ctx, pool, testGameDraft, testSessionUserID, testAuthorID)

	body := getGames(t, srv.URL)

	assertGameIDs(t, body, testGamePublishedOlder)
}

// TestHandleGetGameReturnsTheCallersOwnDraft is the detail half. The list
// showing a card the detail page then refuses would be worse than hiding it in
// both places.
func TestHandleGetGameReturnsTheCallersOwnDraft(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedDraftFixture(t, ctx, pool)

	status, raw := getGame(t, srv.URL, testGameDraft)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, raw)
	}

	var got gameDetail
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	if got.ID != testGameDraft {
		t.Errorf("id = %s, want %s", got.ID, testGameDraft)
	}
	if got.Title != "My own draft" {
		t.Errorf("title = %q, want %q", got.Title, "My own draft")
	}
}

// TestHandleGetGameHidesAnotherAuthorsDraft pins 404 rather than 403. The
// distinction is the whole point: 403 would confirm the row exists, and a draft
// nobody has published is not something a stranger gets to learn about.
func TestHandleGetGameHidesAnotherAuthorsDraft(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedDraftFixture(t, ctx, pool)

	status, body := getGame(t, srv.URL, testGamePublishedNewer)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusNotFound, body)
	}
}

// TestHandleGamesSearchShowsTheCallerTheirOwnDraft covers the third query
// carrying the rule. Search ranks and pages differently from the list, so it
// reaches the predicate by its own path and a fix applied to only one of them
// would pass everything above.
func TestHandleGamesSearchShowsTheCallerTheirOwnDraft(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedSearchFixture(t, ctx, pool)

	// seedSearchFixture credits testAuthorID throughout, including on the
	// unpublished "Balão Secreto". Handing that one row to the caller is the
	// only difference between this test and the search tests next door, which
	// assert the same query without it.
	reassignAuthor(t, ctx, pool, testGameDraft, testAuthorID, testSessionUserID)

	body := getGamesFiltered(t, srv.URL, "query=bal%C3%A3o")

	var found bool
	ids := make([]string, len(body.Games))
	for i, g := range body.Games {
		ids[i] = g.ID
		if g.ID == testGameDraft {
			found = true
		}
	}

	if !found {
		t.Errorf("search returned %v, want it to include the caller's draft %s",
			ids, testGameDraft)
	}
}

// reassignAuthor moves the credit for one game from one account to another, so
// a test can take a fixture written for a different purpose and change the only
// fact these tests care about. The two statements are ordered delete-then-
// insert because game_authors is keyed on the pair, and a game must never reach
// commit without an author.
func reassignAuthor(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	gameID, from, to string,
) {
	t.Helper()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("could not begin the transaction to recredit game %s: %v", gameID, err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx,
		`DELETE FROM game_authors WHERE game_id = $1 AND user_id = $2`, gameID, from)
	if err != nil {
		t.Fatalf("could not drop %s from game %s: %v", from, gameID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("game %s was not credited to %s, so the test is not asserting what it reads",
			gameID, from)
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO game_authors (game_id, user_id) VALUES ($1, $2)`, gameID, to)
	if err != nil {
		t.Fatalf("could not credit %s on game %s: %v", to, gameID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("could not commit the recredit of game %s: %v", gameID, err)
	}
}
