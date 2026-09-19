package api

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"testing"
	"time"
)

// getGame performs the request and hands back the status alongside the body, so
// the error-path tests can assert on a 404 or a 400 rather than fataling the
// way getGames does.
func getGame(t *testing.T, baseURL, id string) (int, []byte) {
	t.Helper()

	res, err := authedGet(t, baseURL+"/v1/games/"+id)
	if err != nil {
		t.Fatalf("GET /v1/games/%s: %v", id, err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("could not read the response body: %v", err)
	}

	return res.StatusCode, body
}

// TestHandleGetGameReturnsTheRow pins the wire shape of the happy path. It is
// the same envelope-free summary the list endpoint emits, so a client reading a
// card and a client reading a game see one shape.
func TestHandleGetGameReturnsTheRow(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)

	want := gameSummary{
		ID:              testGamePublishedNewer,
		Title:           "Human Knot",
		Description:     "Untangle the circle without letting go.",
		Image:           ptr("https://example.test/knot.png"),
		MinParticipants: ptr(int32(6)),
		MaxParticipants: ptr(int32(16)),
		DurationMin:     ptr(int32(10)),
		DurationMax:     ptr(int32(15)),
		NoMaterials:     true,
		Authors:         testAuthors(),
	}

	// A second row the request must not return, so a query that ignores its
	// argument fails here instead of passing by luck.
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedOlder,
		Title:           "Not the one asked for",
		MinParticipants: ptr(int32(2)),
		MaxParticipants: ptr(int32(4)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(10)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	})
	insertGame(t, ctx, pool, testGame{
		ID:              want.ID,
		Title:           want.Title,
		Description:     want.Description,
		Image:           want.Image,
		MinParticipants: want.MinParticipants,
		MaxParticipants: want.MaxParticipants,
		DurationMin:     want.DurationMin,
		DurationMax:     want.DurationMax,
		NoMaterials:     want.NoMaterials,
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC),
	})

	status, body := getGame(t, srv.URL, want.ID)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, body)
	}

	var got gameSummary
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	assertSummary(t, got, want)
}

// TestHandleGetGameKeepsVariantNullsNull is the null-vs-zero trap the list
// endpoint also guards: a variant carries no participants or duration of its
// own, and the response must say null rather than claim zero.
func TestHandleGetGameKeepsVariantNullsNull(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)

	original := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "The original",
		MinParticipants: ptr(int32(6)),
		MaxParticipants: ptr(int32(16)),
		DurationMin:     ptr(int32(10)),
		DurationMax:     ptr(int32(15)),
		PublishState:    "published",
		CreatedAt:       original,
	})
	insertGame(t, ctx, pool, testGame{
		ID:           testGameVariant,
		Title:        "A variant with nothing of its own",
		PublishState: "published",
		OriginalID:   ptr(testGamePublishedNewer),
		CreatedAt:    original.Add(time.Hour),
	})

	status, body := getGame(t, srv.URL, testGameVariant)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, body)
	}

	var got gameSummary
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	assertSummary(t, got, gameSummary{
		ID:      testGameVariant,
		Title:   "A variant with nothing of its own",
		Authors: testAuthors(),
	})
}

// TestHandleGetGameUnknownID is the path that only works because the store
// collects exactly one row: an empty result has to surface as pgx.ErrNoRows for
// classify to turn it into ErrNotFound, and a slice-collecting query would
// panic or return a zero value instead.
func TestHandleGetGameUnknownID(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "The only game in the table",
		MinParticipants: ptr(int32(2)),
		MaxParticipants: ptr(int32(4)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(10)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	})

	status, body := getGame(t, srv.URL, testGameMissing)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusNotFound, body)
	}
}

// TestHandleGetGameMalformedID guards the reason the handler parses the id
// before querying: Postgres answers a malformed uuid with 22P02, which classify
// does not recognise and writeStoreError would report as a 500.
func TestHandleGetGameMalformedID(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	for _, id := range []string{
		"not-a-uuid",
		"60000000-0000-7000-8000-0000000000zz",
		"60000000-0000-7000-8000",
	} {
		t.Run(id, func(t *testing.T) {
			status, body := getGame(t, srv.URL, id)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body %s)", status, http.StatusBadRequest, body)
			}

			var got map[string]string
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("could not decode the response body: %v", err)
			}
			if got["error"] == "" {
				t.Errorf("body = %s, want an error message", body)
			}
		})
	}
}

// TestHandleGetGameIncludesAssociations is what the detail payload exists for:
// both join tables come back on the game, and each category carries the family
// it belongs to.
func TestHandleGetGameIncludesAssociations(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "Human Knot",
		MinParticipants: ptr(int32(6)),
		MaxParticipants: ptr(int32(16)),
		DurationMin:     ptr(int32(10)),
		DurationMax:     ptr(int32(15)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	})

	// The energetic category is written first and belongs to the family with
	// the higher display_order, so a response in insertion order fails here.
	active := gameCategory{
		ID:           testCategoryActive,
		Name:         "Active",
		Description:  "Gets everyone moving.",
		Active:       true,
		DisplayOrder: 1,
		FamilyID:     testFamilyEnergy,
		FamilyName:   "Energy",
	}
	icebreaker := gameCategory{
		ID:           testCategoryIcebreaker,
		Name:         "Icebreaker",
		Description:  "Opens a group that has just met.",
		Active:       true,
		DisplayOrder: 2,
		FamilyID:     testFamilyPurpose,
		FamilyName:   "Purpose",
	}
	indoor := gameLocation{ID: testLocationIndoor, Name: "Indoor"}

	insertCategory(t, ctx, pool, active, 2)
	insertCategory(t, ctx, pool, icebreaker, 1)
	insertLocation(t, ctx, pool, indoor)

	linkGameCategory(t, ctx, pool, testGamePublishedNewer, active.ID)
	linkGameCategory(t, ctx, pool, testGamePublishedNewer, icebreaker.ID)
	linkGameLocation(t, ctx, pool, testGamePublishedNewer, indoor.ID)

	// A second game holding the same rows, so a query that ignores its
	// argument and returns every link fails here.
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedOlder,
		Title:           "Not the one asked for",
		MinParticipants: ptr(int32(2)),
		MaxParticipants: ptr(int32(4)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(10)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC),
	})
	linkGameCategory(t, ctx, pool, testGamePublishedOlder, active.ID)
	linkGameLocation(t, ctx, pool, testGamePublishedOlder, indoor.ID)

	status, body := getGame(t, srv.URL, testGamePublishedNewer)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, body)
	}

	var got gameDetail
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	want := []gameCategory{icebreaker, active}
	if len(got.Categories) != len(want) {
		t.Fatalf("got %d categories %+v, want %d", len(got.Categories), got.Categories, len(want))
	}
	for i := range want {
		if got.Categories[i] != want[i] {
			t.Errorf("categories[%d] = %+v, want %+v", i, got.Categories[i], want[i])
		}
	}

	if len(got.Locations) != 1 {
		t.Fatalf("got %d locations %+v, want 1", len(got.Locations), got.Locations)
	}
	if got.Locations[0] != indoor {
		t.Errorf("locations[0] = %+v, want %+v", got.Locations[0], indoor)
	}
}

// TestHandleGetGameEmptyAssociations guards the make-with-zero-length in
// newGameDetail: a game with no links must answer with [] rather than null, so
// the frontend never branches on the difference.
func TestHandleGetGameEmptyAssociations(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "Unclassified",
		MinParticipants: ptr(int32(2)),
		MaxParticipants: ptr(int32(4)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(10)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	})

	status, body := getGame(t, srv.URL, testGamePublishedNewer)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, body)
	}

	// RawMessage rather than the response struct: null and [] both decode to a
	// nil slice, so only the bytes tell them apart.
	var got struct {
		Categories json.RawMessage `json:"categories"`
		Locations  json.RawMessage `json:"locations"`
		Materials  json.RawMessage `json:"materials"`
		Blocks     json.RawMessage `json:"blocks"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	if s := string(got.Categories); s != "[]" {
		t.Errorf("categories = %s, want []", s)
	}
	if s := string(got.Locations); s != "[]" {
		t.Errorf("locations = %s, want []", s)
	}
	if s := string(got.Materials); s != "[]" {
		t.Errorf("materials = %s, want []", s)
	}
	if s := string(got.Blocks); s != "[]" {
		t.Errorf("blocks = %s, want []", s)
	}
}

// TestHandleGetGameIncludesMaterialQuantities is the round trip issue #16 asks
// for: quantity_base, quantity_per_participant and optional are what make a kit
// list renderable, and all three live on the join row rather than the material,
// so a query that forgot to select them still returns a plausible-looking name.
func TestHandleGetGameIncludesMaterialQuantities(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "Blindfold Maze",
		MinParticipants: ptr(int32(4)),
		MaxParticipants: ptr(int32(12)),
		DurationMin:     ptr(int32(10)),
		DurationMax:     ptr(int32(20)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	})

	// The three columns are given three different values per material, so a
	// mapping that crossed two of them fails here. Rope is written first and
	// sorts second, so a response in insertion order fails too.
	rope := gameMaterial{
		ID:                     testMaterialRope,
		Name:                   "Rope",
		Description:            "Roughly 10 metres, soft enough to hold.",
		QuantityBase:           2,
		QuantityPerParticipant: 0,
		Optional:               true,
	}
	blindfold := gameMaterial{
		ID:                     testMaterialBlindfold,
		Name:                   "Blindfold",
		Description:            "An opaque cloth or sleep mask.",
		QuantityBase:           0,
		QuantityPerParticipant: 1,
		Optional:               false,
	}

	insertMaterial(t, ctx, pool, rope)
	insertMaterial(t, ctx, pool, blindfold)
	linkGameMaterial(t, ctx, pool, testGamePublishedNewer, rope)
	linkGameMaterial(t, ctx, pool, testGamePublishedNewer, blindfold)

	// A second game holding the same material at a different quantity, so a
	// query that ignores its argument fails rather than passing by coincidence.
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedOlder,
		Title:           "Not the one asked for",
		MinParticipants: ptr(int32(2)),
		MaxParticipants: ptr(int32(4)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(10)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC),
	})
	other := rope
	other.QuantityBase = 99
	linkGameMaterial(t, ctx, pool, testGamePublishedOlder, other)

	status, body := getGame(t, srv.URL, testGamePublishedNewer)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, body)
	}

	var got gameDetail
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	want := []gameMaterial{blindfold, rope}
	if len(got.Materials) != len(want) {
		t.Fatalf("got %d materials %+v, want %d", len(got.Materials), got.Materials, len(want))
	}
	for i := range want {
		if got.Materials[i] != want[i] {
			t.Errorf("materials[%d] = %+v, want %+v", i, got.Materials[i], want[i])
		}
	}

	if got.NoMaterials {
		t.Errorf("no_materials = true, want false for a game that needs materials")
	}
}

// TestHandleGetGameNoMaterialsIsDistinguishable covers the other half of issue
// #16: "needs nothing" and "nobody has filled the list in yet" are different
// states, and both answer with an empty array. Only the flag separates them, so
// a client that reads the array alone cannot tell a deliberate empty kit from
// an incomplete record.
func TestHandleGetGameNoMaterialsIsDistinguishable(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)

	// Declared to need nothing.
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "Circle of Words",
		MinParticipants: ptr(int32(4)),
		MaxParticipants: ptr(int32(20)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(10)),
		NoMaterials:     true,
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	})

	// Not declared, and nothing linked yet.
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedOlder,
		Title:           "Unfinished",
		MinParticipants: ptr(int32(2)),
		MaxParticipants: ptr(int32(4)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(10)),
		NoMaterials:     false,
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC),
	})

	for _, tc := range []struct {
		name           string
		gameID         string
		wantNoMaterial bool
	}{
		{"declared to need nothing", testGamePublishedNewer, true},
		{"list not filled in yet", testGamePublishedOlder, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := getGame(t, srv.URL, tc.gameID)
			if status != http.StatusOK {
				t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, body)
			}

			// RawMessage for the array, for the reason
			// TestHandleGetGameEmptyAssociations gives: null and [] both
			// decode to a nil slice.
			var got struct {
				NoMaterials bool            `json:"no_materials"`
				Materials   json.RawMessage `json:"materials"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("could not decode the response body: %v", err)
			}

			if s := string(got.Materials); s != "[]" {
				t.Errorf("materials = %s, want []", s)
			}
			if got.NoMaterials != tc.wantNoMaterial {
				t.Errorf("no_materials = %t, want %t", got.NoMaterials, tc.wantNoMaterial)
			}
		})
	}
}

// TestHandleGetGameIncludesBlocksInPositionOrder is the round trip issue #17
// asks for: blocks are a document, and position is the only thing that makes
// them one. They are written here in the order 2, 0, 1, so a query that leans
// on insertion order — or on the id, which is a uuidv7 and therefore sorts by
// insertion time — fails rather than passing by coincidence.
func TestHandleGetGameIncludesBlocksInPositionOrder(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "Human Knot",
		MinParticipants: ptr(int32(6)),
		MaxParticipants: ptr(int32(16)),
		DurationMin:     ptr(int32(10)),
		DurationMax:     ptr(int32(15)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	})

	heading := gameBlock{
		ID:       testBlockHeading,
		Type:     "heading",
		Content:  "Como jogar",
		Position: 0,
	}
	intro := gameBlock{
		ID:       testBlockIntro,
		Type:     "paragraph",
		Content:  "O grupo forma um círculo apertado.",
		Position: 1,
	}
	steps := gameBlock{
		ID:       testBlockSteps,
		Type:     "steps",
		Content:  "1. Dar as mãos.\n2. Desfazer o nó.",
		Position: 2,
	}

	insertBlock(t, ctx, pool, testGamePublishedNewer, steps)
	insertBlock(t, ctx, pool, testGamePublishedNewer, heading)
	insertBlock(t, ctx, pool, testGamePublishedNewer, intro)

	// A second game whose block sits at position 0, so a query that ignores its
	// argument would sort it to the front and fail here.
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedOlder,
		Title:           "Not the one asked for",
		MinParticipants: ptr(int32(2)),
		MaxParticipants: ptr(int32(4)),
		DurationMin:     ptr(int32(5)),
		DurationMax:     ptr(int32(10)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC),
	})
	insertBlock(t, ctx, pool, testGamePublishedOlder, gameBlock{
		ID:       "70000000-0000-7000-8000-0000000000ff",
		Type:     "paragraph",
		Content:  "Belongs to another game.",
		Position: 0,
	})

	status, body := getGame(t, srv.URL, testGamePublishedNewer)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, body)
	}

	var got gameDetail
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}

	want := []gameBlock{heading, intro, steps}
	if len(got.Blocks) != len(want) {
		t.Fatalf("got %d blocks %+v, want %d", len(got.Blocks), got.Blocks, len(want))
	}
	for i := range want {
		if got.Blocks[i] != want[i] {
			t.Errorf("blocks[%d] = %+v, want %+v", i, got.Blocks[i], want[i])
		}
	}
}

// TestHandleGetGameOmitsBlockSearchColumn holds the other half of issue #17:
// blocks.search is a generated tsvector, an index artefact no client has any
// use for. Decoding into gameDetail would drop an extra key silently, so the
// assertion is on the raw keys the handler actually wrote.
func TestHandleGetGameOmitsBlockSearchColumn(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)
	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "Human Knot",
		MinParticipants: ptr(int32(6)),
		MaxParticipants: ptr(int32(16)),
		DurationMin:     ptr(int32(10)),
		DurationMax:     ptr(int32(15)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	})
	insertBlock(t, ctx, pool, testGamePublishedNewer, gameBlock{
		ID:       testBlockIntro,
		Type:     "paragraph",
		Content:  "O grupo forma um círculo apertado.",
		Position: 0,
	})

	status, body := getGame(t, srv.URL, testGamePublishedNewer)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", status, http.StatusOK, body)
	}

	var got struct {
		Blocks []map[string]json.RawMessage `json:"blocks"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("could not decode the response body: %v", err)
	}
	if len(got.Blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(got.Blocks))
	}

	wantKeys := []string{"id", "type", "content", "position"}
	if len(got.Blocks[0]) != len(wantKeys) {
		t.Errorf("blocks[0] has keys %v, want exactly %v", keysOf(got.Blocks[0]), wantKeys)
	}
	for _, k := range wantKeys {
		if _, ok := got.Blocks[0][k]; !ok {
			t.Errorf("blocks[0] is missing the key %q", k)
		}
	}
	if _, ok := got.Blocks[0]["search"]; ok {
		t.Errorf("blocks[0] carries the generated search column")
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
