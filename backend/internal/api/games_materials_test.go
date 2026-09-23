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

// PUT /v1/games/{id}/materials is the categories endpoint with a payload: the
// link carries quantities and a flag, so a material the body names again is
// rewritten rather than left alone. That upsert is what most of the tests
// below are watching.

// materialGame is the row these tests write to: authored by the caller, so
// nothing here turns on authorship except the tests that mean to.
const materialGame = testGamePublishedOlder

// testMaterialCones is a third material, left unlinked by the fixture so a
// replace has somewhere to go.
const testMaterialCones = "50000000-0000-7000-8000-0000000000a3"

// seedMaterialsFixture writes the game with two of the three materials linked,
// each with quantities a replace can be seen to change.
func seedMaterialsFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	insertGame(t, ctx, pool, testGame{
		ID:              materialGame,
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

	seedMaterialRows(t, ctx, pool)

	linkGameMaterial(t, ctx, pool, materialGame, gameMaterial{
		ID:           testMaterialRope,
		QuantityBase: 1,
	})
	linkGameMaterial(t, ctx, pool, materialGame, gameMaterial{
		ID:                     testMaterialBlindfold,
		QuantityPerParticipant: 1,
		Optional:               true,
	})
}

// seedMaterialRows writes the three materials. Blindfold sorts first by name,
// which the response order follows.
func seedMaterialRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	insertMaterial(t, ctx, pool, gameMaterial{
		ID: testMaterialRope, Name: "Rope", Description: "A long one.",
	})
	insertMaterial(t, ctx, pool, gameMaterial{
		ID: testMaterialBlindfold, Name: "Blindfold", Description: "Opaque.",
	})
	insertMaterial(t, ctx, pool, gameMaterial{
		ID: testMaterialCones, Name: "Cones", Description: "For marking out.",
	})
}

// putMaterials returns the response untouched, for the tests asserting on a
// refusal.
func putMaterials(t *testing.T, baseURL, id, body string) (int, []byte) {
	t.Helper()

	res, err := authedPut(t, baseURL+"/v1/games/"+id+"/materials", body)
	if err != nil {
		t.Fatalf("PUT /v1/games/%s/materials: %v", id, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("could not read the response body: %v", err)
	}

	return res.StatusCode, raw
}

// editMaterials replaces the set expecting success and decodes the game.
func editMaterials(t *testing.T, baseURL, id, body string) gameDetail {
	t.Helper()

	status, raw := putMaterials(t, baseURL, id, body)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, raw)
	}

	var got gameDetail
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	return got
}

func materialIDsOf(materials []gameMaterial) []string {
	ids := make([]string, len(materials))
	for i, m := range materials {
		ids[i] = m.ID
	}

	return ids
}

func assertGameMaterialIDs(t *testing.T, got []gameMaterial, want ...string) {
	t.Helper()

	ids := materialIDsOf(got)

	if len(ids) != len(want) {
		t.Fatalf("got %d materials %v, want %d %v", len(ids), ids, len(want), want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("materials[%d].id = %s, want %s (full order %v)", i, ids[i], want[i], ids)
		}
	}
}

// readMaterials reads the links straight out of the join table, payload and
// all, so a test can check what was stored rather than what the response said
// was stored.
func readMaterials(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	gameID string,
) []gameMaterial {
	t.Helper()

	rows, err := pool.Query(ctx, `
		SELECT material_id, quantity_base, quantity_per_participant, optional
		FROM game_materials
		WHERE game_id = $1 ORDER BY material_id`, gameID)
	if err != nil {
		t.Fatalf("could not read the materials back: %v", err)
	}
	defer rows.Close()

	var materials []gameMaterial
	for rows.Next() {
		var m gameMaterial
		err := rows.Scan(&m.ID, &m.QuantityBase, &m.QuantityPerParticipant, &m.Optional)
		if err != nil {
			t.Fatalf("could not scan a material link: %v", err)
		}
		materials = append(materials, m)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("could not read the materials back: %v", err)
	}

	return materials
}

func assertStoredMaterials(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	gameID string,
	want ...string,
) {
	t.Helper()

	got := materialIDsOf(readMaterials(t, ctx, pool, gameID))
	slices.Sort(want)

	if !slices.Equal(got, want) {
		t.Errorf("stored materials = %v, want %v", got, want)
	}
}

// storedMaterial picks one link out for the tests asserting on the payload.
func storedMaterial(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	gameID, materialID string,
) gameMaterial {
	t.Helper()

	for _, m := range readMaterials(t, ctx, pool, gameID) {
		if m.ID == materialID {
			return m
		}
	}

	t.Fatalf("material %s is not linked to game %s", materialID, gameID)

	return gameMaterial{}
}

// TestHandleEditGameMaterialsReplacesTheSet is the case the endpoint exists
// for: the body names one material the game did not have, and both the ones it
// did are gone.
func TestHandleEditGameMaterialsReplacesTheSet(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedMaterialsFixture(t, ctx, pool)

	got := editMaterials(t, srv.URL, materialGame, `{"materials": [
		{"id": "`+testMaterialCones+`", "quantity_base": 8,
		 "quantity_per_participant": 0, "optional": false}
	]}`)

	assertGameMaterialIDs(t, got.Materials, testMaterialCones)
	assertStoredMaterials(t, ctx, pool, materialGame, testMaterialCones)

	stored := storedMaterial(t, ctx, pool, materialGame, testMaterialCones)
	if stored.QuantityBase != 8 {
		t.Errorf("quantity_base = %d, want 8", stored.QuantityBase)
	}
}

// TestHandleEditGameMaterialsUpdatesAMaterialItAlreadyHad is the difference
// between this endpoint and the other two: the same material listed again is
// not left alone, it is rewritten with the quantities the request carries.
// This is the ON CONFLICT DO UPDATE arm, and DO NOTHING would fail it.
func TestHandleEditGameMaterialsUpdatesAMaterialItAlreadyHad(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedMaterialsFixture(t, ctx, pool)

	// The fixture linked the rope as one, not per participant, not optional.
	got := editMaterials(t, srv.URL, materialGame, `{"materials": [
		{"id": "`+testMaterialRope+`", "quantity_base": 2,
		 "quantity_per_participant": 3, "optional": true}
	]}`)

	assertGameMaterialIDs(t, got.Materials, testMaterialRope)

	stored := storedMaterial(t, ctx, pool, materialGame, testMaterialRope)
	switch {
	case stored.QuantityBase != 2:
		t.Errorf("quantity_base = %d, want the new 2", stored.QuantityBase)
	case stored.QuantityPerParticipant != 3:
		t.Errorf("quantity_per_participant = %d, want the new 3",
			stored.QuantityPerParticipant)
	case !stored.Optional:
		t.Errorf("optional = false, want the new true")
	}

	// And the response said the same thing the table does.
	if got.Materials[0].QuantityBase != 2 || !got.Materials[0].Optional {
		t.Errorf("response material = %+v, want the new quantities", got.Materials[0])
	}
}

// TestHandleEditGameMaterialsKeepsAddsAndDropsOmissions covers the three
// things one request can do at once.
func TestHandleEditGameMaterialsKeepsAddsAndDropsOmissions(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedMaterialsFixture(t, ctx, pool)

	got := editMaterials(t, srv.URL, materialGame, `{"materials": [
		{"id": "`+testMaterialRope+`", "quantity_base": 1,
		 "quantity_per_participant": 0, "optional": false},
		{"id": "`+testMaterialCones+`", "quantity_base": 8,
		 "quantity_per_participant": 0, "optional": false}
	]}`)

	// Cones sorts ahead of Rope by name, whatever order the request used.
	assertGameMaterialIDs(t, got.Materials, testMaterialCones, testMaterialRope)
	assertStoredMaterials(t, ctx, pool, materialGame, testMaterialRope, testMaterialCones)
}

// TestHandleEditGameMaterialsAcceptsAnEmptyList is a replace with nothing in
// it, which is how an editor clears a game.
func TestHandleEditGameMaterialsAcceptsAnEmptyList(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedMaterialsFixture(t, ctx, pool)

	got := editMaterials(t, srv.URL, materialGame, `{"materials": []}`)

	if len(got.Materials) != 0 {
		t.Errorf("materials = %v, want an empty list", materialIDsOf(got.Materials))
	}
	if got.Materials == nil {
		t.Errorf("materials = null, want an empty array")
	}
	if stored := readMaterials(t, ctx, pool, materialGame); len(stored) != 0 {
		t.Errorf("stored materials = %v, want none", materialIDsOf(stored))
	}
}

// TestHandleEditGameMaterialsRefusesABodyWithoutTheField separates "replace
// with nothing" from "did not say". It is also the case that panicked before:
// dereferencing the field without checking it was there.
func TestHandleEditGameMaterialsRefusesABodyWithoutTheField(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"absent", `{}`},
		{"null", `{"materials": null}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, pool := newAuthedTestServer(t)
			ctx := testContext(t)

			seedMaterialsFixture(t, ctx, pool)

			status, raw := putMaterials(t, srv.URL, materialGame, tc.body)
			if status != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want %d (body %s)",
					status, http.StatusUnprocessableEntity, raw)
			}

			assertStoredMaterials(t, ctx, pool, materialGame,
				testMaterialRope, testMaterialBlindfold)
		})
	}
}

// TestHandleEditGameMaterialsRefusesAnUnknownMaterial is the 422 the issue
// asks for, with the id named so the client can tell which one.
func TestHandleEditGameMaterialsRefusesAnUnknownMaterial(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedMaterialsFixture(t, ctx, pool)

	const missing = "50000000-0000-7000-8000-0000000000ff"

	status, raw := putMaterials(t, srv.URL, materialGame, `{"materials": [
		{"id": "`+missing+`", "quantity_base": 1,
		 "quantity_per_participant": 0, "optional": false}
	]}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusUnprocessableEntity, raw)
	}
	if !strings.Contains(string(raw), missing) {
		t.Errorf("body = %s, want it to name the material %s", raw, missing)
	}

	assertStoredMaterials(t, ctx, pool, materialGame,
		testMaterialRope, testMaterialBlindfold)
}

// TestHandleEditGameMaterialsRefusesAQuantitylessRequirement covers the check
// the table carries: a requirement that resolves to nothing is not a
// requirement. The handler refuses it by index rather than letting
// game_materials_quantity_positive answer for it.
func TestHandleEditGameMaterialsRefusesAQuantitylessRequirement(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"both zero", `{"quantity_base": 0, "quantity_per_participant": 0}`},
		{"negative base", `{"quantity_base": -1, "quantity_per_participant": 2}`},
		{"negative per participant", `{"quantity_base": 1, "quantity_per_participant": -2}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, pool := newAuthedTestServer(t)
			ctx := testContext(t)

			seedMaterialsFixture(t, ctx, pool)

			body := `{"materials": [{"id": "` + testMaterialCones + `", ` +
				strings.TrimPrefix(tc.body, "{")

			status, raw := putMaterials(t, srv.URL, materialGame, body+"]}")
			if status != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want %d (body %s)",
					status, http.StatusUnprocessableEntity, raw)
			}
			if !strings.Contains(string(raw), "materials[0]") {
				t.Errorf("body = %s, want it to point at the entry that was wrong", raw)
			}

			assertStoredMaterials(t, ctx, pool, materialGame,
				testMaterialRope, testMaterialBlindfold)
		})
	}
}

// TestHandleEditGameMaterialsRefusesAMalformedID keeps a value that is not a
// uuid out of the query.
func TestHandleEditGameMaterialsRefusesAMalformedID(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedMaterialsFixture(t, ctx, pool)

	status, raw := putMaterials(t, srv.URL, materialGame, `{"materials": [
		{"id": "not-a-uuid", "quantity_base": 1,
		 "quantity_per_participant": 0, "optional": false}
	]}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusUnprocessableEntity, raw)
	}
	if !strings.Contains(string(raw), "materials[0].id") {
		t.Errorf("body = %s, want it to point at the entry that was wrong", raw)
	}
}

// TestHandleEditGameMaterialsRefusesARepeatedID matters more here than for the
// other two: the same material twice with different quantities has no answer,
// and the upsert would quietly let the last one win.
func TestHandleEditGameMaterialsRefusesARepeatedID(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedMaterialsFixture(t, ctx, pool)

	status, raw := putMaterials(t, srv.URL, materialGame, `{"materials": [
		{"id": "`+testMaterialCones+`", "quantity_base": 4,
		 "quantity_per_participant": 0, "optional": false},
		{"id": "`+testMaterialCones+`", "quantity_base": 9,
		 "quantity_per_participant": 0, "optional": false}
	]}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusUnprocessableEntity, raw)
	}

	assertStoredMaterials(t, ctx, pool, materialGame,
		testMaterialRope, testMaterialBlindfold)
}

func TestHandleEditGameMaterialsRejectsAMalformedGameID(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	status, raw := putMaterials(t, srv.URL, "not-a-uuid", `{"materials": []}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusBadRequest, raw)
	}
}

func TestHandleEditGameMaterialsRejectsAMissingGame(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	status, raw := putMaterials(t, srv.URL, testGameMissing, `{"materials": []}`)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusNotFound, raw)
	}
}

func TestHandleEditGameMaterialsNeedsASession(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedMaterialsFixture(t, ctx, pool)

	req, err := http.NewRequest(http.MethodPut,
		srv.URL+"/v1/games/"+materialGame+"/materials", strings.NewReader(`{"materials": []}`))
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

	assertStoredMaterials(t, ctx, pool, materialGame,
		testMaterialRope, testMaterialBlindfold)
}

func TestHandleEditGameMaterialsRejectsANonAuthor(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedSomeoneElsesGame(t, ctx, pool)
	seedMaterialRows(t, ctx, pool)
	linkGameMaterial(t, ctx, pool, testGamePublishedNewer, gameMaterial{
		ID:           testMaterialRope,
		QuantityBase: 1,
	})

	status, raw := putMaterials(t, srv.URL, testGamePublishedNewer, `{"materials": []}`)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusForbidden, raw)
	}

	assertStoredMaterials(t, ctx, pool, testGamePublishedNewer, testMaterialRope)
}

// TestEditGameMaterialsStatementRefusesANonAuthor reaches past the handler on
// purpose, the same way the other two do.
func TestEditGameMaterialsStatementRefusesANonAuthor(t *testing.T) {
	_, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedSomeoneElsesGame(t, ctx, pool)
	seedMaterialRows(t, ctx, pool)
	linkGameMaterial(t, ctx, pool, testGamePublishedNewer, gameMaterial{
		ID:           testMaterialRope,
		QuantityBase: 1,
	})

	_, err := store.EditGameMaterials(ctx, pool, testGamePublishedNewer,
		[]store.GameMaterialInput{}, testSessionUserID)
	if !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("EditGameMaterials() error = %v, want %v", err, store.ErrForbidden)
	}

	assertStoredMaterials(t, ctx, pool, testGamePublishedNewer, testMaterialRope)
}
