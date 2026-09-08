package api

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// One material beyond the game fixtures' Rope and Blindfold. Its id sorts last
// while its name sorts first, so neither an id sort nor an insertion order can
// pass for alphabetical by accident.
const testMaterialBall = "50000000-0000-7000-8000-0000000000a3"

// getMaterials performs the request and decodes the body, failing on anything
// but a 200 with a JSON content type.
func getMaterials(t *testing.T, baseURL string) materialsResponse {
	t.Helper()

	res, err := http.Get(baseURL + "/v1/materials")
	if err != nil {
		t.Fatalf("GET /v1/materials: %v", err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("could not read the response body: %v", err)
	}

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/materials status = %d, want %d (body %s)",
			res.StatusCode, http.StatusOK, raw)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}

	var body materialsResponse
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	return body
}

// TestHandleMaterialsListOrdersByName pins the order the material picker
// renders. Ball is inserted last and carries the highest id, so a query that
// dropped its ORDER BY would return it last.
func TestHandleMaterialsListOrdersByName(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	insertMaterial(t, ctx, pool, gameMaterial{
		ID: testMaterialRope, Name: "Rope", Description: "A long rope"})
	insertMaterial(t, ctx, pool, gameMaterial{
		ID: testMaterialBlindfold, Name: "Blindfold", Description: "An opaque blindfold"})
	insertMaterial(t, ctx, pool, gameMaterial{
		ID: testMaterialBall, Name: "Ball", Description: "A soft ball"})

	body := getMaterials(t, srv.URL)

	assertMaterialIDs(t, body, testMaterialBall, testMaterialBlindfold, testMaterialRope)

	if got := body.Materials[0].Name; got != "Ball" {
		t.Errorf("materials[0].name = %q, want %q", got, "Ball")
	}
}

// TestHandleMaterialsListCarriesDescriptions checks the description travels
// with each material, since the picker shows it next to the name. The empty
// description is covered too: the column defaults to ”, not NULL.
func TestHandleMaterialsListCarriesDescriptions(t *testing.T) {
	srv, pool := newTestServer(t)
	ctx := testContext(t)

	insertMaterial(t, ctx, pool, gameMaterial{
		ID: testMaterialBlindfold, Name: "Blindfold", Description: "An opaque blindfold"})
	insertMaterial(t, ctx, pool, gameMaterial{
		ID: testMaterialRope, Name: "Rope"})

	body := getMaterials(t, srv.URL)

	assertMaterialIDs(t, body, testMaterialBlindfold, testMaterialRope)

	if got, want := body.Materials[0].Description, "An opaque blindfold"; got != want {
		t.Errorf("materials[0].description = %q, want %q", got, want)
	}
	if got := body.Materials[1].Description; got != "" {
		t.Errorf("materials[1].description = %q, want an empty string", got)
	}
}

// TestHandleMaterialsListIsEmptyWithoutRows checks the empty case serialises
// as [] rather than null, so a client can iterate the response unconditionally.
func TestHandleMaterialsListIsEmptyWithoutRows(t *testing.T) {
	srv, _ := newTestServer(t)

	res, err := http.Get(srv.URL + "/v1/materials")
	if err != nil {
		t.Fatalf("GET /v1/materials: %v", err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("could not read the response body: %v", err)
	}

	if got, want := string(raw), `{"materials":[]}`; got != want+"\n" && got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func assertMaterialIDs(t *testing.T, body materialsResponse, want ...string) {
	t.Helper()

	got := make([]string, len(body.Materials))
	for i, m := range body.Materials {
		got[i] = m.ID
	}

	if len(got) != len(want) {
		t.Fatalf("got %d materials %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("materials[%d].id = %s, want %s (full order %v)", i, got[i], want[i], got)
		}
	}
}
