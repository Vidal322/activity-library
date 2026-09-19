package api

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// Two more categories than the game fixtures need: one to make the order
// within a family non-trivial, one that is inactive.
const (
	testCategoryCalm    = "30000000-0000-7000-8000-0000000000a3"
	testCategoryRetired = "30000000-0000-7000-8000-0000000000a4"

	testFamilyEmpty = "20000000-0000-7000-8000-0000000000a3"
)

// getCategories performs the request and decodes the body, failing on anything
// but a 200 with a JSON content type.
func getCategories(t *testing.T, baseURL string) categoriesResponse {
	t.Helper()

	res, err := authedGet(t, baseURL+"/v1/categories")
	if err != nil {
		t.Fatalf("GET /v1/categories: %v", err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("could not read the response body: %v", err)
	}

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/categories status = %d, want %d (body %s)",
			res.StatusCode, http.StatusOK, raw)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}

	var body categoriesResponse
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	return body
}

// The fixture's display_order contradicts both the names and the ids, so a
// query that dropped its ORDER BY cannot pass by luck: Energy sorts first on
// display_order while its id sorts second, and within Energy, Calm sorts first
// on display_order while its id sorts second.
func TestHandleCategoriesListGroupsAndOrders(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	insertCategory(t, ctx, pool, gameCategory{
		ID:           testCategoryActive,
		Name:         "Active",
		Description:  "Games that get people moving.",
		Active:       true,
		DisplayOrder: 2,
		FamilyID:     testFamilyEnergy,
		FamilyName:   "Energy",
	}, 1)
	insertCategory(t, ctx, pool, gameCategory{
		ID:           testCategoryCalm,
		Name:         "Calm",
		Description:  "Games that wind a group down.",
		Active:       true,
		DisplayOrder: 1,
		FamilyID:     testFamilyEnergy,
		FamilyName:   "Energy",
	}, 1)
	insertCategory(t, ctx, pool, gameCategory{
		ID:           testCategoryIcebreaker,
		Name:         "Icebreaker",
		Description:  "Games for a group that has just met.",
		Active:       true,
		DisplayOrder: 1,
		FamilyID:     testFamilyPurpose,
		FamilyName:   "Purpose",
	}, 2)

	// Active is false, so this one must not appear even though its family does.
	insertCategory(t, ctx, pool, gameCategory{
		ID:           testCategoryRetired,
		Name:         "Retired",
		Active:       false,
		DisplayOrder: 2,
		FamilyID:     testFamilyPurpose,
		FamilyName:   "Purpose",
	}, 2)

	body := getCategories(t, srv.URL)

	assertFamilyIDs(t, body, testFamilyEnergy, testFamilyPurpose)

	assertCategoryIDs(t, body.Families[0], testCategoryCalm, testCategoryActive)
	assertCategoryIDs(t, body.Families[1], testCategoryIcebreaker)

	energy := body.Families[0]
	if energy.Name != "Energy" {
		t.Errorf("families[0].name = %q, want %q", energy.Name, "Energy")
	}
	if energy.DisplayOrder != 1 {
		t.Errorf("families[0].display_order = %d, want 1", energy.DisplayOrder)
	}

	calm := energy.Categories[0]
	if calm.Name != "Calm" {
		t.Errorf("families[0].categories[0].name = %q, want %q", calm.Name, "Calm")
	}
	if calm.Description != "Games that wind a group down." {
		t.Errorf("families[0].categories[0].description = %q, want the seeded one",
			calm.Description)
	}
	if calm.DisplayOrder != 1 {
		t.Errorf("families[0].categories[0].display_order = %d, want 1", calm.DisplayOrder)
	}
}

// TestHandleCategoriesListOmitsAFamilyWithNoActiveCategories pins the inner
// join: the rail should not render a section header with nothing under it.
func TestHandleCategoriesListOmitsAFamilyWithNoActiveCategories(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	insertCategory(t, ctx, pool, gameCategory{
		ID:           testCategoryIcebreaker,
		Name:         "Icebreaker",
		Active:       true,
		DisplayOrder: 1,
		FamilyID:     testFamilyPurpose,
		FamilyName:   "Purpose",
	}, 1)
	insertCategory(t, ctx, pool, gameCategory{
		ID:           testCategoryRetired,
		Name:         "Retired",
		Active:       false,
		DisplayOrder: 1,
		FamilyID:     testFamilyEmpty,
		FamilyName:   "Nothing Active",
	}, 2)

	assertFamilyIDs(t, getCategories(t, srv.URL), testFamilyPurpose)
}

// TestHandleCategoriesListIsEmptyWithoutRows checks the empty case serialises
// as [] rather than null, so a client can iterate the response unconditionally.
func TestHandleCategoriesListIsEmptyWithoutRows(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	res, err := authedGet(t, srv.URL+"/v1/categories")
	if err != nil {
		t.Fatalf("GET /v1/categories: %v", err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("could not read the response body: %v", err)
	}

	if got, want := string(raw), `{"families":[]}`; got != want+"\n" && got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func assertFamilyIDs(t *testing.T, body categoriesResponse, want ...string) {
	t.Helper()

	got := make([]string, len(body.Families))
	for i, f := range body.Families {
		got[i] = f.ID
	}

	if len(got) != len(want) {
		t.Fatalf("got %d families %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("families[%d].id = %s, want %s (full order %v)", i, got[i], want[i], got)
		}
	}
}

func assertCategoryIDs(t *testing.T, family categoryFamily, want ...string) {
	t.Helper()

	got := make([]string, len(family.Categories))
	for i, c := range family.Categories {
		got[i] = c.ID
	}

	if len(got) != len(want) {
		t.Fatalf("family %s: got %d categories %v, want %d %v",
			family.Name, len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("family %s: categories[%d].id = %s, want %s (full order %v)",
				family.Name, i, got[i], want[i], got)
		}
	}
}
