package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Vidal322/activity-library/internal/store"
)

// PUT /v1/games/{id}/categories replaces the set rather than adding to it, so
// what every test below watches is which links survived: one the body names is
// kept, one it leaves out is gone, and one it names for the first time is new.

// categoryGame is the row these tests write to: authored by the caller, so
// nothing here turns on authorship except the tests that mean to.
const categoryGame = testGamePublishedOlder

// seedCategoriesFixture writes the game with two of the three categories
// linked. The third is left unlinked so a replace has somewhere to go.
func seedCategoriesFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	insertGame(t, ctx, pool, testGame{
		ID:              categoryGame,
		Title:           "Jogo do Lenço",
		Description:     "Two teams, one handkerchief",
		MinParticipants: ptr(int32(6)),
		MaxParticipants: ptr(int32(30)),
		DurationMin:     ptr(int32(15)),
		DurationMax:     ptr(int32(40)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Authors:         []string{testSessionUserID},
	})

	seedCategoryRows(t, ctx, pool)

	linkGameCategory(t, ctx, pool, categoryGame, testCategoryIcebreaker)
	linkGameCategory(t, ctx, pool, categoryGame, testCategoryActive)
}

// seedCategoryRows writes the three categories and their two families. The
// display orders are laid out so the response order — family first, then the
// category's own order — is calm, icebreaker, active, which is neither the
// insertion order nor the id order.
func seedCategoryRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	insertCategory(t, ctx, pool, gameCategory{
		ID:           testCategoryActive,
		Name:         "Active",
		Description:  "Gets everyone moving.",
		Active:       true,
		DisplayOrder: 1,
		FamilyID:     testFamilyEnergy,
		FamilyName:   "Energy",
	}, 2)

	insertCategory(t, ctx, pool, gameCategory{
		ID:           testCategoryIcebreaker,
		Name:         "Icebreaker",
		Description:  "Opens a group that has just met.",
		Active:       true,
		DisplayOrder: 2,
		FamilyID:     testFamilyPurpose,
		FamilyName:   "Purpose",
	}, 1)

	insertCategory(t, ctx, pool, gameCategory{
		ID:           testCategoryCalm,
		Name:         "Calm",
		Description:  "Winds a group back down.",
		Active:       true,
		DisplayOrder: 1,
		FamilyID:     testFamilyPurpose,
		FamilyName:   "Purpose",
	}, 1)
}

// putCategories returns the response untouched, for the tests asserting on a
// refusal.
func putCategories(t *testing.T, baseURL, id, body string) (int, []byte) {
	t.Helper()

	res, err := authedPut(t, baseURL+"/v1/games/"+id+"/categories", body)
	if err != nil {
		t.Fatalf("PUT /v1/games/%s/categories: %v", id, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("could not read the response body: %v", err)
	}

	return res.StatusCode, raw
}

// editCategories replaces the set expecting success and decodes the game.
func editCategories(t *testing.T, baseURL, id, body string) gameDetail {
	t.Helper()

	status, raw := putCategories(t, baseURL, id, body)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, raw)
	}

	var got gameDetail
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	return got
}

func categoryIDsOf(categories []gameCategory) []string {
	ids := make([]string, len(categories))
	for i, c := range categories {
		ids[i] = c.ID
	}

	return ids
}

// assertGameCategoryIDs compares the listed ids in order, since the response
// promises the grouping the client renders without sorting for itself.
func assertGameCategoryIDs(t *testing.T, got []gameCategory, want ...string) {
	t.Helper()

	ids := categoryIDsOf(got)

	if len(ids) != len(want) {
		t.Fatalf("got %d categories %v, want %d %v", len(ids), ids, len(want), want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("categories[%d].id = %s, want %s (full order %v)", i, ids[i], want[i], ids)
		}
	}
}

// readCategories reads the links straight out of the join table, so a test can
// check what was stored rather than what the response said was stored. The
// order is the id's rather than the response's: this is the set, not the view
// of it.
func readCategories(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	gameID string,
) []string {
	t.Helper()

	rows, err := pool.Query(ctx, `
		SELECT category_id FROM game_categories
		WHERE game_id = $1 ORDER BY category_id`, gameID)
	if err != nil {
		t.Fatalf("could not read the categories back: %v", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("could not scan a category link: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("could not read the categories back: %v", err)
	}

	return ids
}

func assertStoredCategories(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	gameID string,
	want ...string,
) {
	t.Helper()

	got := readCategories(t, ctx, pool, gameID)
	slices.Sort(want)

	if !slices.Equal(got, want) {
		t.Errorf("stored categories = %v, want %v", got, want)
	}
}

// linkedAt reads when a link was written, so the replace can be held to
// keeping the rows it was never asked to touch rather than deleting and
// reinserting the lot.
func linkedAt(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	gameID, categoryID string,
) time.Time {
	t.Helper()

	var at time.Time
	err := pool.QueryRow(ctx, `
		SELECT created_at FROM game_categories
		WHERE game_id = $1 AND category_id = $2`, gameID, categoryID).Scan(&at)
	if err != nil {
		t.Fatalf("could not read created_at for category %s: %v", categoryID, err)
	}

	return at
}

// TestHandleEditGameCategoriesReplacesTheSet is the case the endpoint exists
// for: the body names one category the game did not have, and both the ones it
// did are gone. An implementation that appended would answer with three.
func TestHandleEditGameCategoriesReplacesTheSet(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedCategoriesFixture(t, ctx, pool)

	got := editCategories(t, srv.URL, categoryGame,
		`{"categories": ["`+testCategoryCalm+`"]}`)

	assertGameCategoryIDs(t, got.Categories, testCategoryCalm)
	assertStoredCategories(t, ctx, pool, categoryGame, testCategoryCalm)
}

// TestHandleEditGameCategoriesKeepsAddsAndDropsOmissions covers the three
// things one request can do at once, and holds the kept link to being the same
// row rather than a fresh one wearing the same id.
func TestHandleEditGameCategoriesKeepsAddsAndDropsOmissions(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedCategoriesFixture(t, ctx, pool)
	before := linkedAt(t, ctx, pool, categoryGame, testCategoryActive)

	got := editCategories(t, srv.URL, categoryGame,
		`{"categories": ["`+testCategoryActive+`", "`+testCategoryCalm+`"]}`)

	// Calm comes first: its family sorts ahead of Energy, whatever order the
	// request listed the two in.
	assertGameCategoryIDs(t, got.Categories, testCategoryCalm, testCategoryActive)
	assertStoredCategories(t, ctx, pool, categoryGame, testCategoryActive, testCategoryCalm)

	if after := linkedAt(t, ctx, pool, categoryGame, testCategoryActive); !after.Equal(before) {
		t.Errorf("the kept link's created_at = %v, want the original %v", after, before)
	}
}

// TestHandleEditGameCategoriesAcceptsAnEmptyList is a replace with nothing in
// it, which is how an editor clears a game. It has to be distinguishable from
// a body that forgot the field, which the test below covers.
func TestHandleEditGameCategoriesAcceptsAnEmptyList(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedCategoriesFixture(t, ctx, pool)

	got := editCategories(t, srv.URL, categoryGame, `{"categories": []}`)

	if len(got.Categories) != 0 {
		t.Errorf("categories = %v, want an empty list", categoryIDsOf(got.Categories))
	}
	if got.Categories == nil {
		t.Errorf("categories = null, want an empty array")
	}
	if stored := readCategories(t, ctx, pool, categoryGame); len(stored) != 0 {
		t.Errorf("stored categories = %v, want none", stored)
	}
}

// TestHandleEditGameCategoriesRefusesABodyWithoutTheField separates "replace
// with nothing" from "did not say", which an implementation reading the field
// into a plain slice would collapse into the first and silently clear the game.
func TestHandleEditGameCategoriesRefusesABodyWithoutTheField(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"absent", `{}`},
		{"null", `{"categories": null}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, pool := newAuthedTestServer(t)
			ctx := testContext(t)

			seedCategoriesFixture(t, ctx, pool)

			status, raw := putCategories(t, srv.URL, categoryGame, tc.body)
			if status != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want %d (body %s)",
					status, http.StatusUnprocessableEntity, raw)
			}

			assertStoredCategories(t, ctx, pool, categoryGame,
				testCategoryIcebreaker, testCategoryActive)
		})
	}
}

// TestHandleEditGameCategoriesRefusesAnUnknownCategory is the 422 the issue
// asks for: a well formed uuid naming no category is a bad list, not a broken
// server, and the answer names the id so the client can tell which one.
func TestHandleEditGameCategoriesRefusesAnUnknownCategory(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedCategoriesFixture(t, ctx, pool)

	status, raw := putCategories(t, srv.URL, categoryGame,
		`{"categories": ["`+testCategoryCalm+`", "`+testCategoryMissing+`"]}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusUnprocessableEntity, raw)
	}
	if !strings.Contains(string(raw), testCategoryMissing) {
		t.Errorf("body = %s, want it to name the category %s", raw, testCategoryMissing)
	}

	// The transaction rolled back whole: the two links this request would have
	// dropped are both still there, and the one it would have added is not.
	assertStoredCategories(t, ctx, pool, categoryGame,
		testCategoryIcebreaker, testCategoryActive)
}

// TestHandleEditGameCategoriesRefusesAMalformedID keeps a value that is not a
// uuid out of the query, where it would surface as a failure of the server
// rather than of the request.
func TestHandleEditGameCategoriesRefusesAMalformedID(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedCategoriesFixture(t, ctx, pool)

	status, raw := putCategories(t, srv.URL, categoryGame, `{"categories": ["not-a-uuid"]}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusUnprocessableEntity, raw)
	}
	if !strings.Contains(string(raw), "categories[0]") {
		t.Errorf("body = %s, want it to point at the entry that was wrong", raw)
	}

	assertStoredCategories(t, ctx, pool, categoryGame,
		testCategoryIcebreaker, testCategoryActive)
}

// TestHandleEditGameCategoriesRefusesARepeatedID covers the entry listed twice.
// The set has no room for it, and letting it through would collide with
// game_categories_pkey and answer 409 about a row the caller did mean to have.
func TestHandleEditGameCategoriesRefusesARepeatedID(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedCategoriesFixture(t, ctx, pool)

	status, raw := putCategories(t, srv.URL, categoryGame,
		`{"categories": ["`+testCategoryCalm+`", "`+testCategoryCalm+`"]}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusUnprocessableEntity, raw)
	}

	assertStoredCategories(t, ctx, pool, categoryGame,
		testCategoryIcebreaker, testCategoryActive)
}

// TestHandleEditGameCategoriesRejectsAMalformedGameID stops at the path,
// before the authorship query has a chance to fail on the value instead.
func TestHandleEditGameCategoriesRejectsAMalformedGameID(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	status, raw := putCategories(t, srv.URL, "not-a-uuid", `{"categories": []}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusBadRequest, raw)
	}
}

func TestHandleEditGameCategoriesRejectsAMissingGame(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	status, raw := putCategories(t, srv.URL, testGameMissing, `{"categories": []}`)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusNotFound, raw)
	}
}

func TestHandleEditGameCategoriesNeedsASession(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedCategoriesFixture(t, ctx, pool)

	req, err := http.NewRequest(http.MethodPut,
		srv.URL+"/v1/games/"+categoryGame+"/categories", strings.NewReader(`{"categories": []}`))
	if err != nil {
		t.Fatalf("could not build the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT without a session: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusUnauthorized)
	}

	assertStoredCategories(t, ctx, pool, categoryGame,
		testCategoryIcebreaker, testCategoryActive)
}

func TestHandleEditGameCategoriesRejectsANonAuthor(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedSomeoneElsesGame(t, ctx, pool)
	seedCategoryRows(t, ctx, pool)
	linkGameCategory(t, ctx, pool, testGamePublishedNewer, testCategoryIcebreaker)

	status, raw := putCategories(t, srv.URL, testGamePublishedNewer, `{"categories": []}`)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusForbidden, raw)
	}

	assertStoredCategories(t, ctx, pool, testGamePublishedNewer, testCategoryIcebreaker)
}

// TestEditGameCategoriesStatementRefusesANonAuthor reaches past the handler on
// purpose, the same way the blocks tests do: the handler's own check would let
// every test above pass against a transaction that happily rewrote anyone's
// game.
func TestEditGameCategoriesStatementRefusesANonAuthor(t *testing.T) {
	_, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedSomeoneElsesGame(t, ctx, pool)
	seedCategoryRows(t, ctx, pool)
	linkGameCategory(t, ctx, pool, testGamePublishedNewer, testCategoryIcebreaker)

	_, err := store.EditGameCategories(ctx, pool, testGamePublishedNewer,
		[]string{}, testSessionUserID)
	if !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("EditGameCategories() error = %v, want %v", err, store.ErrForbidden)
	}

	assertStoredCategories(t, ctx, pool, testGamePublishedNewer, testCategoryIcebreaker)
}
