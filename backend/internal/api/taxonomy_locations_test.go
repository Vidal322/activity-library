package api

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// One location beyond the game fixtures' Indoor and Outdoor. Its id sorts last
// while its name sorts first, so neither an id sort nor an insertion order can
// pass for alphabetical by accident.
const testLocationBeach = "40000000-0000-7000-8000-0000000000a3"

// getLocations performs the request and decodes the body, failing on anything
// but a 200 with a JSON content type.
func getLocations(t *testing.T, baseURL string) locationsResponse {
	t.Helper()

	res, err := authedGet(t, baseURL+"/v1/locations")
	if err != nil {
		t.Fatalf("GET /v1/locations: %v", err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("could not read the response body: %v", err)
	}

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/locations status = %d, want %d (body %s)",
			res.StatusCode, http.StatusOK, raw)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}

	var body locationsResponse
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	return body
}

// TestHandleLocationsListOrdersByName pins the order the filter rail renders.
// Beach is inserted last and carries the highest id, so a query that dropped
// its ORDER BY would return it last.
func TestHandleLocationsListOrdersByName(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	insertLocation(t, ctx, pool, gameLocation{ID: testLocationIndoor, Name: "Indoor"})
	insertLocation(t, ctx, pool, gameLocation{ID: testLocationOutdoor, Name: "Outdoor"})
	insertLocation(t, ctx, pool, gameLocation{ID: testLocationBeach, Name: "Beach"})

	body := getLocations(t, srv.URL)

	assertLocationIDs(t, body, testLocationBeach, testLocationIndoor, testLocationOutdoor)

	if got := body.Locations[0].Name; got != "Beach" {
		t.Errorf("locations[0].name = %q, want %q", got, "Beach")
	}
}

// TestHandleLocationsListIsEmptyWithoutRows checks the empty case serialises
// as [] rather than null, so a client can iterate the response unconditionally.
func TestHandleLocationsListIsEmptyWithoutRows(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	res, err := authedGet(t, srv.URL+"/v1/locations")
	if err != nil {
		t.Fatalf("GET /v1/locations: %v", err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("could not read the response body: %v", err)
	}

	if got, want := string(raw), `{"locations":[]}`; got != want+"\n" && got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func assertLocationIDs(t *testing.T, body locationsResponse, want ...string) {
	t.Helper()

	got := make([]string, len(body.Locations))
	for i, l := range body.Locations {
		got[i] = l.ID
	}

	if len(got) != len(want) {
		t.Fatalf("got %d locations %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("locations[%d].id = %s, want %s (full order %v)", i, got[i], want[i], got)
		}
	}
}
