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

// PUT /v1/games/{id}/locations replaces the set rather than adding to it. It
// is the categories endpoint without the family, so the tests below are the
// same shape, minus the ordering the grouping imposes.

// locationGame is the row these tests write to: authored by the caller, so
// nothing here turns on authorship except the tests that mean to.
const locationGame = testGamePublishedOlder

// seedLocationsFixture writes the game with two of the three locations linked.
// The third is left unlinked so a replace has somewhere to go.
func seedLocationsFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	insertGame(t, ctx, pool, testGame{
		ID:              locationGame,
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

	seedLocationRows(t, ctx, pool)

	linkGameLocation(t, ctx, pool, locationGame, testLocationIndoor)
	linkGameLocation(t, ctx, pool, locationGame, testLocationOutdoor)
}

// seedLocationRows writes the three locations. Beach sorts ahead of the other
// two by name, so a replace that lands on it is visible in the response order
// as well as in the set.
func seedLocationRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	insertLocation(t, ctx, pool, gameLocation{ID: testLocationIndoor, Name: "Indoor"})
	insertLocation(t, ctx, pool, gameLocation{ID: testLocationOutdoor, Name: "Outdoor"})
	insertLocation(t, ctx, pool, gameLocation{ID: testLocationBeach, Name: "Beach"})
}

// putLocations returns the response untouched, for the tests asserting on a
// refusal.
func putLocations(t *testing.T, baseURL, id, body string) (int, []byte) {
	t.Helper()

	res, err := authedPut(t, baseURL+"/v1/games/"+id+"/locations", body)
	if err != nil {
		t.Fatalf("PUT /v1/games/%s/locations: %v", id, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("could not read the response body: %v", err)
	}

	return res.StatusCode, raw
}

// editLocations replaces the set expecting success and decodes the game.
func editLocations(t *testing.T, baseURL, id, body string) gameDetail {
	t.Helper()

	status, raw := putLocations(t, baseURL, id, body)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, raw)
	}

	var got gameDetail
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	return got
}

func locationIDsOf(locations []gameLocation) []string {
	ids := make([]string, len(locations))
	for i, l := range locations {
		ids[i] = l.ID
	}

	return ids
}

// assertGameLocationIDs compares the listed ids in order, since the response
// promises them by name and a replace must not disturb that.
func assertGameLocationIDs(t *testing.T, got []gameLocation, want ...string) {
	t.Helper()

	ids := locationIDsOf(got)

	if len(ids) != len(want) {
		t.Fatalf("got %d locations %v, want %d %v", len(ids), ids, len(want), want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("locations[%d].id = %s, want %s (full order %v)", i, ids[i], want[i], ids)
		}
	}
}

// readLocations reads the links straight out of the join table, so a test can
// check what was stored rather than what the response said was stored.
func readLocations(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	gameID string,
) []string {
	t.Helper()

	rows, err := pool.Query(ctx, `
		SELECT location_id FROM game_locations
		WHERE game_id = $1 ORDER BY location_id`, gameID)
	if err != nil {
		t.Fatalf("could not read the locations back: %v", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("could not scan a location link: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("could not read the locations back: %v", err)
	}

	return ids
}

func assertStoredLocations(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	gameID string,
	want ...string,
) {
	t.Helper()

	got := readLocations(t, ctx, pool, gameID)
	slices.Sort(want)

	if !slices.Equal(got, want) {
		t.Errorf("stored locations = %v, want %v", got, want)
	}
}

// locationLinkedAt reads when a link was written, so the replace can be held
// to keeping the rows it was never asked to touch.
func locationLinkedAt(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	gameID, locationID string,
) time.Time {
	t.Helper()

	var at time.Time
	err := pool.QueryRow(ctx, `
		SELECT created_at FROM game_locations
		WHERE game_id = $1 AND location_id = $2`, gameID, locationID).Scan(&at)
	if err != nil {
		t.Fatalf("could not read created_at for location %s: %v", locationID, err)
	}

	return at
}

// TestHandleEditGameLocationsReplacesTheSet is the case the endpoint exists
// for: the body names one location the game did not have, and both the ones it
// did are gone. An implementation that appended would answer with three.
func TestHandleEditGameLocationsReplacesTheSet(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedLocationsFixture(t, ctx, pool)

	got := editLocations(t, srv.URL, locationGame,
		`{"locations": ["`+testLocationBeach+`"]}`)

	assertGameLocationIDs(t, got.Locations, testLocationBeach)
	assertStoredLocations(t, ctx, pool, locationGame, testLocationBeach)
}

// TestHandleEditGameLocationsKeepsAddsAndDropsOmissions covers the three
// things one request can do at once, and holds the kept link to being the same
// row rather than a fresh one wearing the same id.
func TestHandleEditGameLocationsKeepsAddsAndDropsOmissions(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedLocationsFixture(t, ctx, pool)
	before := locationLinkedAt(t, ctx, pool, locationGame, testLocationIndoor)

	got := editLocations(t, srv.URL, locationGame,
		`{"locations": ["`+testLocationIndoor+`", "`+testLocationBeach+`"]}`)

	// Beach comes first: the response is ordered by name, whatever order the
	// request listed the two in.
	assertGameLocationIDs(t, got.Locations, testLocationBeach, testLocationIndoor)
	assertStoredLocations(t, ctx, pool, locationGame, testLocationIndoor, testLocationBeach)

	after := locationLinkedAt(t, ctx, pool, locationGame, testLocationIndoor)
	if !after.Equal(before) {
		t.Errorf("the kept link's created_at = %v, want the original %v", after, before)
	}
}

// TestHandleEditGameLocationsAcceptsAnEmptyList is a replace with nothing in
// it, which is how an editor clears a game.
func TestHandleEditGameLocationsAcceptsAnEmptyList(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedLocationsFixture(t, ctx, pool)

	got := editLocations(t, srv.URL, locationGame, `{"locations": []}`)

	if len(got.Locations) != 0 {
		t.Errorf("locations = %v, want an empty list", locationIDsOf(got.Locations))
	}
	if got.Locations == nil {
		t.Errorf("locations = null, want an empty array")
	}
	if stored := readLocations(t, ctx, pool, locationGame); len(stored) != 0 {
		t.Errorf("stored locations = %v, want none", stored)
	}
}

// TestHandleEditGameLocationsRefusesABodyWithoutTheField separates "replace
// with nothing" from "did not say", which an implementation reading the field
// into a plain slice would collapse into the first and silently clear the game.
func TestHandleEditGameLocationsRefusesABodyWithoutTheField(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"absent", `{}`},
		{"null", `{"locations": null}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, pool := newAuthedTestServer(t)
			ctx := testContext(t)

			seedLocationsFixture(t, ctx, pool)

			status, raw := putLocations(t, srv.URL, locationGame, tc.body)
			if status != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want %d (body %s)",
					status, http.StatusUnprocessableEntity, raw)
			}

			assertStoredLocations(t, ctx, pool, locationGame,
				testLocationIndoor, testLocationOutdoor)
		})
	}
}

// TestHandleEditGameLocationsRefusesAnUnknownLocation is the 422 the issue
// asks for: a well formed uuid naming no location is a bad list, not a broken
// server, and the answer names the id so the client can tell which one.
func TestHandleEditGameLocationsRefusesAnUnknownLocation(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedLocationsFixture(t, ctx, pool)

	status, raw := putLocations(t, srv.URL, locationGame,
		`{"locations": ["`+testLocationBeach+`", "`+testLocationMissing+`"]}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusUnprocessableEntity, raw)
	}
	if !strings.Contains(string(raw), testLocationMissing) {
		t.Errorf("body = %s, want it to name the location %s", raw, testLocationMissing)
	}

	// The transaction rolled back whole: the two links this request would have
	// dropped are both still there, and the one it would have added is not.
	assertStoredLocations(t, ctx, pool, locationGame,
		testLocationIndoor, testLocationOutdoor)
}

// TestHandleEditGameLocationsRefusesAMalformedID keeps a value that is not a
// uuid out of the query, where it would surface as a failure of the server
// rather than of the request.
func TestHandleEditGameLocationsRefusesAMalformedID(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedLocationsFixture(t, ctx, pool)

	status, raw := putLocations(t, srv.URL, locationGame, `{"locations": ["not-a-uuid"]}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusUnprocessableEntity, raw)
	}
	if !strings.Contains(string(raw), "locations[0]") {
		t.Errorf("body = %s, want it to point at the entry that was wrong", raw)
	}

	assertStoredLocations(t, ctx, pool, locationGame,
		testLocationIndoor, testLocationOutdoor)
}

// TestHandleEditGameLocationsRefusesARepeatedID covers the entry listed twice.
// The set has no room for it, and letting it through would collide with
// game_locations_pkey and answer 409 about a row the caller did mean to have.
func TestHandleEditGameLocationsRefusesARepeatedID(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedLocationsFixture(t, ctx, pool)

	status, raw := putLocations(t, srv.URL, locationGame,
		`{"locations": ["`+testLocationBeach+`", "`+testLocationBeach+`"]}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusUnprocessableEntity, raw)
	}

	assertStoredLocations(t, ctx, pool, locationGame,
		testLocationIndoor, testLocationOutdoor)
}

// TestHandleEditGameLocationsLeavesTheCategoriesAlone is the one thing the
// categories tests cannot check: the two endpoints write neighbouring join
// tables, and a replace that reached the wrong one would pass every test above.
func TestHandleEditGameLocationsLeavesTheCategoriesAlone(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedLocationsFixture(t, ctx, pool)
	seedCategoryRows(t, ctx, pool)
	linkGameCategory(t, ctx, pool, locationGame, testCategoryIcebreaker)

	editLocations(t, srv.URL, locationGame, `{"locations": []}`)

	assertStoredCategories(t, ctx, pool, locationGame, testCategoryIcebreaker)
}

// TestHandleEditGameLocationsRejectsAMalformedGameID stops at the path, before
// the authorship query has a chance to fail on the value instead.
func TestHandleEditGameLocationsRejectsAMalformedGameID(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	status, raw := putLocations(t, srv.URL, "not-a-uuid", `{"locations": []}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusBadRequest, raw)
	}
}

func TestHandleEditGameLocationsRejectsAMissingGame(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	status, raw := putLocations(t, srv.URL, testGameMissing, `{"locations": []}`)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusNotFound, raw)
	}
}

func TestHandleEditGameLocationsNeedsASession(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedLocationsFixture(t, ctx, pool)

	req, err := http.NewRequest(http.MethodPut,
		srv.URL+"/v1/games/"+locationGame+"/locations", strings.NewReader(`{"locations": []}`))
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

	assertStoredLocations(t, ctx, pool, locationGame,
		testLocationIndoor, testLocationOutdoor)
}

func TestHandleEditGameLocationsRejectsANonAuthor(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedSomeoneElsesGame(t, ctx, pool)
	seedLocationRows(t, ctx, pool)
	linkGameLocation(t, ctx, pool, testGamePublishedNewer, testLocationIndoor)

	status, raw := putLocations(t, srv.URL, testGamePublishedNewer, `{"locations": []}`)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusForbidden, raw)
	}

	assertStoredLocations(t, ctx, pool, testGamePublishedNewer, testLocationIndoor)
}

// TestEditGameLocationsStatementRefusesANonAuthor reaches past the handler on
// purpose: the handler's own check would let every test above pass against a
// transaction that happily rewrote anyone's game.
func TestEditGameLocationsStatementRefusesANonAuthor(t *testing.T) {
	_, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedSomeoneElsesGame(t, ctx, pool)
	seedLocationRows(t, ctx, pool)
	linkGameLocation(t, ctx, pool, testGamePublishedNewer, testLocationIndoor)

	_, err := store.EditGameLocations(ctx, pool, testGamePublishedNewer,
		[]string{}, testSessionUserID)
	if !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("EditGameLocations() error = %v, want %v", err, store.ErrForbidden)
	}

	assertStoredLocations(t, ctx, pool, testGamePublishedNewer, testLocationIndoor)
}
