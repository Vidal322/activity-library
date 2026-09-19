package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// seedFilterFixture writes the rows the filter tests read: three published
// games with overlapping categories and locations, plus a draft that carries
// every association and is the newest row, so a filter that forgot the publish
// state would lead its list with it.
//
//	newer  Icebreaker, Active   Indoor
//	third  Active               Indoor, Outdoor
//	older  Icebreaker           Outdoor
//	draft  Icebreaker, Active   Indoor, Outdoor
func seedFilterFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	insertAuthor(t, ctx, pool, testAuthorID)

	insertCategory(t, ctx, pool, gameCategory{
		ID:           testCategoryIcebreaker,
		Name:         "Icebreaker",
		Description:  "Helps a new group get talking.",
		Active:       true,
		DisplayOrder: 0,
		FamilyID:     testFamilyPurpose,
		FamilyName:   "Purpose",
	}, 0)
	insertCategory(t, ctx, pool, gameCategory{
		ID:           testCategoryActive,
		Name:         "Active",
		Description:  "Raises the energy of the group.",
		Active:       true,
		DisplayOrder: 0,
		FamilyID:     testFamilyEnergy,
		FamilyName:   "Energy",
	}, 1)

	insertLocation(t, ctx, pool, gameLocation{ID: testLocationIndoor, Name: "Indoor"})
	insertLocation(t, ctx, pool, gameLocation{ID: testLocationOutdoor, Name: "Outdoor"})

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	games := []struct {
		id           string
		title        string
		publishState string
		createdAt    time.Time
		categories   []string
		locations    []string
	}{
		{
			id:           testGamePublishedOlder,
			title:        "Circle of Words",
			publishState: "published",
			createdAt:    base,
			categories:   []string{testCategoryIcebreaker},
			locations:    []string{testLocationOutdoor},
		},
		{
			id:           testGamePublishedThird,
			title:        "Blindfold Maze",
			publishState: "published",
			createdAt:    base.Add(time.Hour),
			categories:   []string{testCategoryActive},
			locations:    []string{testLocationIndoor, testLocationOutdoor},
		},
		{
			id:           testGamePublishedNewer,
			title:        "Human Knot",
			publishState: "published",
			createdAt:    base.Add(2 * time.Hour),
			categories:   []string{testCategoryIcebreaker, testCategoryActive},
			locations:    []string{testLocationIndoor},
		},
		{
			id:           testGameDraft,
			title:        "Still a draft",
			publishState: "draft",
			createdAt:    base.Add(3 * time.Hour),
			categories:   []string{testCategoryIcebreaker, testCategoryActive},
			locations:    []string{testLocationIndoor, testLocationOutdoor},
		},
	}

	for _, g := range games {
		insertGame(t, ctx, pool, testGame{
			ID:              g.id,
			Title:           g.title,
			Description:     "a game the filter tests read",
			MinParticipants: ptr(int32(4)),
			MaxParticipants: ptr(int32(12)),
			DurationMin:     ptr(int32(10)),
			DurationMax:     ptr(int32(20)),
			PublishState:    g.publishState,
			CreatedAt:       g.createdAt,
		})

		for _, c := range g.categories {
			linkGameCategory(t, ctx, pool, g.id, c)
		}
		for _, l := range g.locations {
			linkGameLocation(t, ctx, pool, g.id, l)
		}
	}
}

// TestHandleGamesListFiltersByCategory is the base case: only the games
// carrying the category come back, still newest first, still without the draft.
func TestHandleGamesListFiltersByCategory(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedFilterFixture(t, testContext(t), pool)

	body := getGamesFiltered(t, srv.URL, "category="+testCategoryIcebreaker)

	assertGameIDs(t, body, testGamePublishedNewer, testGamePublishedOlder)
}

// TestHandleGamesListANDsCategories is the assertion the filter rail implies:
// two categories narrow the list rather than widening it, so the game carrying
// only one of them drops out.
func TestHandleGamesListANDsCategories(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedFilterFixture(t, testContext(t), pool)

	body := getGamesFiltered(t, srv.URL,
		"category="+testCategoryIcebreaker+"&category="+testCategoryActive)

	assertGameIDs(t, body, testGamePublishedNewer)
}

func TestHandleGamesListFiltersByLocation(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedFilterFixture(t, testContext(t), pool)

	body := getGamesFiltered(t, srv.URL, "location="+testLocationIndoor)

	assertGameIDs(t, body, testGamePublishedNewer, testGamePublishedThird)
}

// TestHandleGamesListCombinesCategoryAndLocation crosses the two fields. Each
// half alone matches two games, and only one game satisfies both.
func TestHandleGamesListCombinesCategoryAndLocation(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedFilterFixture(t, testContext(t), pool)

	body := getGamesFiltered(t, srv.URL,
		"category="+testCategoryActive+"&location="+testLocationOutdoor)

	assertGameIDs(t, body, testGamePublishedThird)
}

// TestHandleGamesListWithoutFilterIsUnchanged pins that the filter is opt-in:
// the parameters absent, the endpoint still lists every published game.
func TestHandleGamesListWithoutFilterIsUnchanged(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedFilterFixture(t, testContext(t), pool)

	body := getGames(t, srv.URL)

	assertGameIDs(t, body,
		testGamePublishedNewer, testGamePublishedThird, testGamePublishedOlder)
}

// TestHandleGamesListDeduplicatesFilterIDs guards the match itself: it counts
// join rows against the length of the requested array, and the join keys are
// unique, so a repeat would push the target out of reach and match nothing.
// The second case is the same id in the spelling pgtype also accepts, which a
// set keyed on the raw string would miss.
func TestHandleGamesListDeduplicatesFilterIDs(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedFilterFixture(t, testContext(t), pool)

	bare := strings.ReplaceAll(testCategoryIcebreaker, "-", "")

	for name, query := range map[string]string{
		"same spelling":      "category=" + testCategoryIcebreaker + "&category=" + testCategoryIcebreaker,
		"different spelling": "category=" + testCategoryIcebreaker + "&category=" + strings.ToUpper(bare),
	} {
		t.Run(name, func(t *testing.T) {
			body := getGamesFiltered(t, srv.URL, query)

			assertGameIDs(t, body, testGamePublishedNewer, testGamePublishedOlder)
		})
	}
}

// TestHandleGamesListRejectsMalformedFilterID keeps a bad id from reaching
// Postgres, which answers 22P02 for a malformed uuid; classify does not
// recognise that code and writeStoreError would report it as a 500.
func TestHandleGamesListRejectsMalformedFilterID(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedFilterFixture(t, testContext(t), pool)

	for name, query := range map[string]string{
		"not a uuid":        "category=nonsense",
		"truncated":         "location=40000000-0000-7000-8000",
		"empty value":       "category=",
		"bad among good":    "category=" + testCategoryIcebreaker + "&category=nonsense",
		"malformed on both": "category=nonsense&location=nonsense",
	} {
		t.Run(name, func(t *testing.T) {
			status, _, raw := getGamesRaw(t, srv.URL, query)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body %s)", status, http.StatusBadRequest, raw)
			}

			var got map[string]string
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("could not decode the response body: %v", err)
			}
			if got["error"] == "" {
				t.Errorf("body = %s, want an error message", raw)
			}
		})
	}
}

// TestHandleGamesListRejectsUnknownFilterID is the half a malformed-id check
// cannot cover: the id is a valid uuid that names no row. Answering an empty
// list would read as a library with nothing in it, so it is a 400 naming the
// id instead.
func TestHandleGamesListRejectsUnknownFilterID(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedFilterFixture(t, testContext(t), pool)

	for name, tc := range map[string]struct{ query, wantID string }{
		"category":         {"category=" + testCategoryMissing, testCategoryMissing},
		"location":         {"location=" + testLocationMissing, testLocationMissing},
		"among known ones": {"category=" + testCategoryIcebreaker + "&category=" + testCategoryMissing, testCategoryMissing},
	} {
		t.Run(name, func(t *testing.T) {
			status, _, raw := getGamesRaw(t, srv.URL, tc.query)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body %s)", status, http.StatusBadRequest, raw)
			}

			var got map[string]string
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("could not decode the response body: %v", err)
			}
			if !strings.Contains(got["error"], tc.wantID) {
				t.Errorf("error = %q, want it to name %s", got["error"], tc.wantID)
			}
		})
	}
}

// seedScalarFixture seeds the participant, duration and no_materials filters.
// It is separate from seedFilterFixture, whose games all share one spine: a
// scalar filter can only be shown to work against bands that differ.
//
// The bands overlap on purpose, so a filter has to select rather than partition:
//
//	                       participants   duration   no kit   category
//	newer  Human Knot          10–20        30–60      no     Icebreaker
//	third  Blindfold Maze      15–30        10–20      yes    Active
//	older  Circle of Words      4–8         30–60      no     Icebreaker
//	draft  Still a draft       10–20        30–60      no     Icebreaker
//
// The gap between 8 and 10 is deliberate: it is the only way to tell a filter
// that matches nothing from one that is not applied.
func seedScalarFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	insertAuthor(t, ctx, pool, testAuthorID)

	insertCategory(t, ctx, pool, gameCategory{
		ID:           testCategoryIcebreaker,
		Name:         "Icebreaker",
		Description:  "Helps a new group get talking.",
		Active:       true,
		DisplayOrder: 0,
		FamilyID:     testFamilyPurpose,
		FamilyName:   "Purpose",
	}, 0)
	insertCategory(t, ctx, pool, gameCategory{
		ID:           testCategoryActive,
		Name:         "Active",
		Description:  "Raises the energy of the group.",
		Active:       true,
		DisplayOrder: 0,
		FamilyID:     testFamilyEnergy,
		FamilyName:   "Energy",
	}, 1)

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	games := []struct {
		id                  string
		title               string
		minPart, maxPart    int32
		durationMin, durMax int32
		noMaterials         bool
		publishState        string
		createdAt           time.Time
		category            string
	}{
		{
			id: testGamePublishedOlder, title: "Circle of Words",
			minPart: 4, maxPart: 8, durationMin: 30, durMax: 60,
			publishState: "published", createdAt: base,
			category: testCategoryIcebreaker,
		},
		{
			id: testGamePublishedThird, title: "Blindfold Maze",
			minPart: 15, maxPart: 30, durationMin: 10, durMax: 20,
			noMaterials:  true,
			publishState: "published", createdAt: base.Add(time.Hour),
			category: testCategoryActive,
		},
		{
			id: testGamePublishedNewer, title: "Human Knot",
			minPart: 10, maxPart: 20, durationMin: 30, durMax: 60,
			publishState: "published", createdAt: base.Add(2 * time.Hour),
			category: testCategoryIcebreaker,
		},
		{
			id: testGameDraft, title: "Still a draft",
			minPart: 10, maxPart: 20, durationMin: 30, durMax: 60,
			publishState: "draft", createdAt: base.Add(3 * time.Hour),
			category: testCategoryIcebreaker,
		},
	}

	for _, g := range games {
		insertGame(t, ctx, pool, testGame{
			ID:              g.id,
			Title:           g.title,
			Description:     "a game the scalar filter tests read",
			MinParticipants: ptr(g.minPart),
			MaxParticipants: ptr(g.maxPart),
			DurationMin:     ptr(g.durationMin),
			DurationMax:     ptr(g.durMax),
			NoMaterials:     g.noMaterials,
			PublishState:    g.publishState,
			CreatedAt:       g.createdAt,
		})

		linkGameCategory(t, ctx, pool, g.id, g.category)
	}
}

// TestHandleGamesListFiltersByParticipants reads the filter the way the rail
// poses it: this is the group I have, not a range to overlap. A game matches
// when its declared band contains the number, and the band is inclusive at both
// ends, so the counsellor with exactly max_participants kids is not turned away.
func TestHandleGamesListFiltersByParticipants(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedScalarFixture(t, testContext(t), pool)

	for name, tc := range map[string]struct {
		value string
		want  []string
	}{
		"inside two bands":            {"15", []string{testGamePublishedNewer, testGamePublishedThird}},
		"lower bound":                 {"10", []string{testGamePublishedNewer}},
		"upper bound":                 {"20", []string{testGamePublishedNewer, testGamePublishedThird}},
		"upper bound of the smallest": {"8", []string{testGamePublishedOlder}},
		"in the gap":                  {"9", nil},
		"above every band":            {"31", nil},
	} {
		t.Run(name, func(t *testing.T) {
			body := getGamesFiltered(t, srv.URL, "participants="+tc.value)

			assertGameIDs(t, body, tc.want...)
		})
	}
}

// TestHandleGamesListFiltersByDuration is the same containment against the
// other pair of columns, on a fixture where the two bands cut the list
// differently from the participant ones.
func TestHandleGamesListFiltersByDuration(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedScalarFixture(t, testContext(t), pool)

	for name, tc := range map[string]struct {
		value string
		want  []string
	}{
		"inside one band": {"45", []string{testGamePublishedNewer, testGamePublishedOlder}},
		"lower bound":     {"30", []string{testGamePublishedNewer, testGamePublishedOlder}},
		"upper bound":     {"60", []string{testGamePublishedNewer, testGamePublishedOlder}},
		"the short game":  {"15", []string{testGamePublishedThird}},
		"in the gap":      {"25", nil},
	} {
		t.Run(name, func(t *testing.T) {
			body := getGamesFiltered(t, srv.URL, "duration="+tc.value)

			assertGameIDs(t, body, tc.want...)
		})
	}
}

// TestHandleGamesListFiltersByNoMaterials pins both directions. false is a
// filter in its own right rather than a filter switched off, which is the only
// reading that lets the chip's two states both be expressible in the URL.
func TestHandleGamesListFiltersByNoMaterials(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedScalarFixture(t, testContext(t), pool)

	for name, tc := range map[string]struct {
		value string
		want  []string
	}{
		"needs nothing": {"true", []string{testGamePublishedThird}},
		"needs a kit":   {"false", []string{testGamePublishedNewer, testGamePublishedOlder}},
	} {
		t.Run(name, func(t *testing.T) {
			body := getGamesFiltered(t, srv.URL, "no_materials="+tc.value)

			assertGameIDs(t, body, tc.want...)
		})
	}
}

// TestHandleGamesListCombinesScalarAndCategoryFilters is the composition the
// rail produces once a counsellor has touched more than one control. Each
// filter alone admits two games and no two of them admit the same pair, so a
// query that answered by honouring only one of them would be visible here.
func TestHandleGamesListCombinesScalarAndCategoryFilters(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedScalarFixture(t, testContext(t), pool)

	for name, tc := range map[string]struct {
		query string
		want  []string
	}{
		"category and participants": {
			"category=" + testCategoryIcebreaker + "&participants=15",
			[]string{testGamePublishedNewer},
		},
		"participants and duration": {
			"participants=15&duration=45",
			[]string{testGamePublishedNewer},
		},
		"category, location, and all three scalars": {
			"category=" + testCategoryActive + "&participants=15&duration=15&no_materials=true",
			[]string{testGamePublishedThird},
		},
		"each half matches, the pair does not": {
			"participants=15&duration=25",
			nil,
		},
		"category excludes what the scalar admits": {
			"category=" + testCategoryActive + "&participants=10",
			nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			body := getGamesFiltered(t, srv.URL, tc.query)

			assertGameIDs(t, body, tc.want...)
		})
	}
}

// TestHandleGamesListScalarFiltersTreatNullBoundsAsUnbounded fixes the choice
// the query had to make about the nullable half of the spine. Only a variant
// can carry nulls, since games_non_variant_complete requires the four columns
// of everything else. A blank bound means nobody said, not does not fit, so it
// is read as no limit on that side rather than as a reason to hide the game.
func TestHandleGamesListScalarFiltersTreatNullBoundsAsUnbounded(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	insertGame(t, ctx, pool, testGame{
		ID:              testGamePublishedNewer,
		Title:           "The original",
		MinParticipants: ptr(int32(10)),
		MaxParticipants: ptr(int32(20)),
		DurationMin:     ptr(int32(30)),
		DurationMax:     ptr(int32(60)),
		PublishState:    "published",
		CreatedAt:       base,
	})
	insertGame(t, ctx, pool, testGame{
		ID:           testGameVariant,
		Title:        "A variant with nothing of its own",
		PublishState: "published",
		OriginalID:   ptr(testGamePublishedNewer),
		CreatedAt:    base.Add(time.Hour),
	})

	for name, tc := range map[string]struct {
		query string
		want  []string
	}{
		"inside the original's band": {
			"participants=15", []string{testGameVariant, testGamePublishedNewer},
		},
		"outside it": {
			"participants=100", []string{testGameVariant},
		},
		"duration outside it": {
			"duration=5", []string{testGameVariant},
		},
	} {
		t.Run(name, func(t *testing.T) {
			body := getGamesFiltered(t, srv.URL, tc.query)

			assertGameIDs(t, body, tc.want...)
		})
	}
}

// TestHandleGamesListRejectsMalformedScalarFilter keeps the three scalars to
// the vocabulary the rail emits. Zero and negatives are refused rather than
// answered with an empty list: every stored bound is above zero by CHECK, so
// they could only ever match nothing, and nothing reads as an empty library.
//
// An empty value is a 400 too. The rail holds its state in the URL, so a
// cleared control has to drop its key rather than send it blank.
func TestHandleGamesListRejectsMalformedScalarFilter(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedScalarFixture(t, testContext(t), pool)

	for name, query := range map[string]string{
		"zero":                  "participants=0",
		"negative":              "participants=-3",
		"not a number":          "participants=abc",
		"fractional":            "participants=1.5",
		"empty value":           "participants=",
		"wider than an int32":   "participants=3000000000",
		"repeated":              "participants=1&participants=2",
		"zero duration":         "duration=0",
		"negative duration":     "duration=-10",
		"not a boolean":         "no_materials=yes",
		"a boolean pg accepts":  "no_materials=1",
		"go's boolean spelling": "no_materials=TRUE",
		"empty boolean":         "no_materials=",
		"repeated boolean":      "no_materials=true&no_materials=false",
		"bad beside a good one": "participants=0&category=" + testCategoryIcebreaker,
	} {
		t.Run(name, func(t *testing.T) {
			status, _, raw := getGamesRaw(t, srv.URL, query)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body %s)", status, http.StatusBadRequest, raw)
			}

			var got map[string]string
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("could not decode the response body: %v", err)
			}

			// The message has to name the parameter: the rail sets several at
			// once, and "not a positive whole number" alone does not say which
			// control to go back to.
			field := strings.SplitN(query, "=", 2)[0]
			if !strings.HasPrefix(got["error"], field+": ") {
				t.Errorf("error = %q, want it to start with %q", got["error"], field+": ")
			}
		})
	}
}
