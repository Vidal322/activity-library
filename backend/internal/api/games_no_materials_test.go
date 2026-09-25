package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// game_materials_game_fkey pins every material row to a game whose
// no_materials is false, so the flag and the rows cannot disagree. It can be
// tripped from either end, and the tests below cover both: adding materials to
// a game marked as needing none, and marking a game that still lists some.
// Neither may reach the client as a raw database error, and neither may leave
// a half-applied write behind.

// seedFlaggedGame writes materialGame marked as needing no materials, with the
// three materials available but none linked, since the schema forbids it.
func seedFlaggedGame(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	insertGame(t, ctx, pool, testGame{
		ID:              materialGame,
		Title:           "Jogo do Lenço",
		Description:     "Two teams, one handkerchief",
		MinParticipants: ptr(int32(6)),
		MaxParticipants: ptr(int32(30)),
		DurationMin:     ptr(int32(15)),
		DurationMax:     ptr(int32(40)),
		NoMaterials:     true,
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Authors:         []string{testSessionUserID},
	})

	seedMaterialRows(t, ctx, pool)
}

func storedNoMaterials(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string) bool {
	t.Helper()

	var flag bool
	err := pool.QueryRow(ctx, `SELECT no_materials FROM games WHERE id = $1`, id).Scan(&flag)
	if err != nil {
		t.Fatalf("could not read no_materials of %s: %v", id, err)
	}

	return flag
}

func TestHandleEditGameMaterialsRefusesAGameMarkedAsNeedingNone(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedFlaggedGame(t, ctx, pool)

	status, raw := putMaterials(t, srv.URL, materialGame, `{"materials": [
		{"id": "`+testMaterialRope+`", "quantity_base": 1,
		 "quantity_per_participant": 0, "optional": false}
	]}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusUnprocessableEntity, raw)
	}

	assertErrorMessage(t, raw,
		"this game is marked as needing no materials; unset no_materials before adding any")

	assertStoredMaterials(t, ctx, pool, materialGame)
}

// TestHandleEditGameMaterialsAcceptsAnEmptyListOnAFlaggedGame keeps the refusal
// above from growing into one against the endpoint as a whole: an empty list is
// what a game needing no materials already has.
func TestHandleEditGameMaterialsAcceptsAnEmptyListOnAFlaggedGame(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedFlaggedGame(t, ctx, pool)

	got := editMaterials(t, srv.URL, materialGame, `{"materials": []}`)
	assertGameMaterialIDs(t, got.Materials)
}

// TestHandleEditGameMaterialsAfterUnsettingTheFlag is the way out the refusal
// message points to.
func TestHandleEditGameMaterialsAfterUnsettingTheFlag(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedFlaggedGame(t, ctx, pool)

	editGame(t, srv.URL, materialGame, `{"no_materials": false}`)

	got := editMaterials(t, srv.URL, materialGame, `{"materials": [
		{"id": "`+testMaterialRope+`", "quantity_base": 1,
		 "quantity_per_participant": 0, "optional": false}
	]}`)
	assertGameMaterialIDs(t, got.Materials, testMaterialRope)
}

// TestHandleEditGameRefusesToFlagAGameThatListsMaterials is the other
// direction. Setting the flag is refused rather than taken as an instruction to
// clear the rows, and the whole PATCH goes with it: the title sent alongside
// is not written either.
func TestHandleEditGameRefusesToFlagAGameThatListsMaterials(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedMaterialsFixture(t, ctx, pool)

	status, raw := patchGame(t, srv.URL, materialGame,
		`{"title": "Renamed", "no_materials": true}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusUnprocessableEntity, raw)
	}

	assertErrorMessage(t, raw,
		"this game still lists materials; remove them before marking it as needing no materials")

	if storedNoMaterials(t, ctx, pool, materialGame) {
		t.Errorf("no_materials = true after a refused PATCH, want false")
	}
	if title := titleOf(t, ctx, pool, materialGame); title != "Jogo do Lenço" {
		t.Errorf("title = %q after a refused PATCH, want it unchanged", title)
	}

	assertStoredMaterials(t, ctx, pool, materialGame,
		testMaterialRope, testMaterialBlindfold)
}

// TestHandleEditGameFlagsAGameOnceItsMaterialsAreGone is the way out that
// refusal points to.
func TestHandleEditGameFlagsAGameOnceItsMaterialsAreGone(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedMaterialsFixture(t, ctx, pool)

	editMaterials(t, srv.URL, materialGame, `{"materials": []}`)

	got := editGame(t, srv.URL, materialGame, `{"no_materials": true}`)
	if !got.NoMaterials {
		t.Errorf("no_materials = false, want true")
	}
	if !storedNoMaterials(t, ctx, pool, materialGame) {
		t.Errorf("stored no_materials = false, want true")
	}
}
