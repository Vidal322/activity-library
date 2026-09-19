package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// seedSearchFixture writes rows whose text, not whose associations, is the
// point. The four published games each carry the search term in a different
// place, so a test can tell a title match from one buried in a block:
//
//	newer  "Jogo do Balão"        term in the title
//	third  "Corrida de Sacos"     term in the description
//	older  "Caça ao Tesouro"      term in a block, and nowhere else
//	draft  "Balão Secreto"        term in the title, unpublished
//
// The accent is deliberate: the pt_unaccent configuration is what has to make
// "balao" reach "balão", and a fixture spelled without one would pass whether
// or not migration 00004's text search configuration is in play.
func seedSearchFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	insertAuthor(t, ctx, pool, testAuthorID)

	insertCategory(t, ctx, pool, gameCategory{
		ID:           testCategoryActive,
		Name:         "Active",
		Description:  "Raises the energy of the group.",
		Active:       true,
		DisplayOrder: 0,
		FamilyID:     testFamilyEnergy,
		FamilyName:   "Energy",
	}, 0)

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	games := []struct {
		id           string
		title        string
		description  string
		publishState string
		createdAt    time.Time
	}{
		{
			id:           testGamePublishedOlder,
			title:        "Caça ao Tesouro",
			description:  "As equipas seguem pistas pelo terreno.",
			publishState: "published",
			createdAt:    base,
		},
		{
			id:           testGamePublishedThird,
			title:        "Corrida de Sacos",
			description:  "Uma corrida com um balão entre os joelhos.",
			publishState: "published",
			createdAt:    base.Add(time.Hour),
		},
		{
			id:           testGamePublishedNewer,
			title:        "Jogo do Balão",
			description:  "As equipas defendem o que trazem ao tornozelo.",
			publishState: "published",
			createdAt:    base.Add(2 * time.Hour),
		},
		{
			id:           testGameDraft,
			title:        "Balão Secreto",
			description:  "Ainda por publicar.",
			publishState: "draft",
			createdAt:    base.Add(3 * time.Hour),
		},
	}

	for _, g := range games {
		insertGame(t, ctx, pool, testGame{
			ID:           g.id,
			Title:        g.title,
			Description:  g.description,
			PublishState: g.publishState,
			CreatedAt:    g.createdAt,
			// The variant columns are the only ones the schema insists on for
			// a non-variant row; the search reads none of them.
			MinParticipants: ptr(int32(4)),
			MaxParticipants: ptr(int32(12)),
			DurationMin:     ptr(int32(10)),
			DurationMax:     ptr(int32(20)),
		})
	}

	// The only occurrence of the term outside a title or description, and the
	// reason migration 00004 aggregates blocks into the document at all.
	insertBlock(t, ctx, pool, testGamePublishedOlder, gameBlock{
		ID:       testBlockIntro,
		Type:     "paragraph",
		Content:  "Uma das pistas está escondida dentro de um balão.",
		Position: 0,
	})

	linkGameCategory(t, ctx, pool, testGamePublishedNewer, testCategoryActive)
	linkGameCategory(t, ctx, pool, testGamePublishedOlder, testCategoryActive)
}

// TestHandleGamesListSearchRanksTitleFirst is the base case, and the reason the
// search path orders by ts_rank_cd rather than by created_at: the title match
// has to lead even though it is not the row a plain list would put first.
func TestHandleGamesListSearchRanksTitleFirst(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedSearchFixture(t, testContext(t), pool)

	body := getGamesFiltered(t, srv.URL, "query=balão")

	// Title (weight A), then description (B), then block prose (C). The draft
	// carries the term in its title and still must not appear.
	assertGameIDs(t, body,
		testGamePublishedNewer, testGamePublishedThird, testGamePublishedOlder)
}

// TestHandleGamesListSearchReachesBlockProse is issue 21's first acceptance
// criterion: a game whose only match is inside a block still comes back, which
// only holds while the blocks trigger keeps game_search.document current.
func TestHandleGamesListSearchReachesBlockProse(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedSearchFixture(t, testContext(t), pool)

	body := getGamesFiltered(t, srv.URL, "query=escondida")

	assertGameIDs(t, body, testGamePublishedOlder)
}

// TestHandleGamesListSearchFoldsAccents is the second acceptance criterion.
// A counsellor types from a phone keyboard without accents and still has to
// find the game.
func TestHandleGamesListSearchFoldsAccents(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedSearchFixture(t, testContext(t), pool)

	body := getGamesFiltered(t, srv.URL, "query=balao")

	assertGameIDs(t, body,
		testGamePublishedNewer, testGamePublishedThird, testGamePublishedOlder)
}

// TestHandleGamesListSearchComposesWithFilters is why the search lives on the
// list endpoint rather than beside it: the filter rail stays live while a
// search is running, so the two have to narrow together.
func TestHandleGamesListSearchComposesWithFilters(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedSearchFixture(t, testContext(t), pool)

	body := getGamesFiltered(t, srv.URL, "query=balão&category="+testCategoryActive)

	// "Corrida de Sacos" matches the search but carries no category, so the
	// filter drops it while the ranked order of the rest survives.
	assertGameIDs(t, body, testGamePublishedNewer, testGamePublishedOlder)
}

// TestHandleGamesListSearchPagesByCursor covers the half of the design that is
// not the query: the search page is positioned by a row count rather than a
// keyset, and the client must not be able to tell.
func TestHandleGamesListSearchPagesByCursor(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedSearchFixture(t, testContext(t), pool)

	first := getGamesFiltered(t, srv.URL, "query=balão&limit=2")
	assertGameIDs(t, first, testGamePublishedNewer, testGamePublishedThird)

	if first.NextCursor == nil {
		t.Fatal("next_cursor is null on a page with a further page to reach")
	}

	second := getGamesFiltered(t, srv.URL,
		"query=balão&limit=2&cursor="+*first.NextCursor)
	assertGameIDs(t, second, testGamePublishedOlder)

	if second.NextCursor != nil {
		t.Errorf("next_cursor = %q on the last page, want null", *second.NextCursor)
	}
}

// TestHandleGamesListSearchRejectsListCursor is what the cursor's kind prefix
// buys. Paging the list and then typing in the search box would otherwise hand
// a keyset cursor to a path that reads row counts, and the offset it decoded to
// would be silent nonsense.
func TestHandleGamesListSearchRejectsListCursor(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedSearchFixture(t, testContext(t), pool)

	list := getGamesFiltered(t, srv.URL, "limit=2")
	if list.NextCursor == nil {
		t.Fatal("the list fixture must span more than one page")
	}

	status, _, raw := getGamesRaw(t, srv.URL, "query=balão&cursor="+*list.NextCursor)

	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (body %s)", status, http.StatusBadRequest, raw)
	}
}

// TestHandleGamesListSearchRejectsEmptyQuery is issue 21's 400: an empty search
// box is the client's to leave off the request entirely.
func TestHandleGamesListSearchRejectsEmptyQuery(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedSearchFixture(t, testContext(t), pool)

	for _, rawQuery := range []string{"query=", "query=%20%20"} {
		t.Run(rawQuery, func(t *testing.T) {
			status, _, raw := getGamesRaw(t, srv.URL, rawQuery)

			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want %d (body %s)",
					status, http.StatusBadRequest, raw)
			}
		})
	}
}

// TestHandleGamesListSearchMatchesNothing keeps the empty result an empty list
// rather than an error: websearch_to_tsquery takes anything a user types, so a
// term nobody wrote is a normal answer.
func TestHandleGamesListSearchMatchesNothing(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedSearchFixture(t, testContext(t), pool)

	body := getGamesFiltered(t, srv.URL, "query=trampolim")

	assertGameIDs(t, body)
	if body.NextCursor != nil {
		t.Errorf("next_cursor = %q on an empty result, want null", *body.NextCursor)
	}
}
