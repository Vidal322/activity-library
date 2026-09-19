package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Vidal322/activity-library/internal/store"
)

// getGamesPage requests one page and returns the ids it carried alongside the
// cursor that reaches the next one, nil on the last page.
func getGamesPage(t *testing.T, baseURL, rawQuery string) ([]string, *string) {
	t.Helper()

	body := getGamesFiltered(t, baseURL, rawQuery)

	ids := make([]string, len(body.Games))
	for i, g := range body.Games {
		ids[i] = g.ID
	}

	return ids, body.NextCursor
}

// pageGames walks from the first page to the last at the given size and returns
// every id it saw, in order. It stops on an absent cursor rather than after a
// page count the test picked, so a cursor that never clears fails here instead
// of running forever.
func pageGames(t *testing.T, baseURL, query string, limit int) []string {
	t.Helper()

	var (
		all    []string
		cursor *string
	)

	for page := 0; ; page++ {
		if page > 20 {
			t.Fatalf("still paging after %d requests, ids so far %v", page, all)
		}

		raw := "limit=" + strconv.Itoa(limit)
		if query != "" {
			raw += "&" + query
		}
		if cursor != nil {
			raw += "&cursor=" + url.QueryEscape(*cursor)
		}

		ids, next := getGamesPage(t, baseURL, raw)
		if len(ids) > limit {
			t.Fatalf("page %d returned %d games, want at most %d", page, len(ids), limit)
		}
		all = append(all, ids...)

		if next == nil {
			return all
		}
		cursor = next
	}
}

// TestHandleGamesListPagesThroughEveryGameExactlyOnce is the assertion the
// issue asks for: a page size of one walks the seeded library, in the order the
// unpaged list promises, with nothing skipped or repeated.
func TestHandleGamesListPagesThroughEveryGameExactlyOnce(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedFilterFixture(t, testContext(t), pool)

	for _, limit := range []int{1, 2, 3, 50} {
		t.Run("limit "+strconv.Itoa(limit), func(t *testing.T) {
			got := pageGames(t, srv.URL, "", limit)

			want := []string{
				testGamePublishedNewer, testGamePublishedThird, testGamePublishedOlder,
			}
			if len(got) != len(want) {
				t.Fatalf("paged %d games %v, want %d %v", len(got), got, len(want), want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("game %d = %s, want %s (full order %v)", i, got[i], want[i], got)
				}
			}
		})
	}
}

// TestHandleGamesListPagesThroughGamesSharingATimestamp is why the cursor
// carries the id as well. created_at alone cannot order these three, so a
// cursor without the tiebreaker would either repeat the whole group or skip
// past it.
func TestHandleGamesListPagesThroughGamesSharingATimestamp(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)
	same := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	for _, id := range []string{
		testGamePublishedNewer, testGamePublishedOlder, testGamePublishedThird,
	} {
		insertGame(t, ctx, pool, testGame{
			ID:              id,
			Title:           "Written in the same instant",
			MinParticipants: ptr(int32(4)),
			MaxParticipants: ptr(int32(12)),
			DurationMin:     ptr(int32(10)),
			DurationMax:     ptr(int32(20)),
			PublishState:    "published",
			CreatedAt:       same,
		})
	}

	got := pageGames(t, srv.URL, "", 1)

	// The ids break the tie, descending like the timestamp.
	want := []string{testGamePublishedThird, testGamePublishedOlder, testGamePublishedNewer}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("paged %v, want %v", got, want)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("paged %d games %v, want %d", len(got), got, len(want))
	}
}

// TestHandleGamesListInsertMidScrollDoesNotDuplicateOrHide is the drift an
// offset would suffer. A game written after the first page is newer than the
// cursor, so it belongs to a part of the list already scrolled past: the rows
// still to come arrive once each, and none is pushed out of reach.
func TestHandleGamesListInsertMidScrollDoesNotDuplicateOrHide(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	seedFilterFixture(t, ctx, pool)

	first, cursor := getGamesPage(t, srv.URL, "limit=1")
	if cursor == nil {
		t.Fatal("the first of three pages carried no cursor")
	}
	if len(first) != 1 || first[0] != testGamePublishedNewer {
		t.Fatalf("first page = %v, want [%s]", first, testGamePublishedNewer)
	}

	insertGame(t, ctx, pool, testGame{
		ID:              testGameVariant,
		Title:           "Written mid-scroll",
		MinParticipants: ptr(int32(4)),
		MaxParticipants: ptr(int32(12)),
		DurationMin:     ptr(int32(10)),
		DurationMax:     ptr(int32(20)),
		PublishState:    "published",
		CreatedAt:       time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
	})

	rest := []string{}
	for cursor != nil {
		ids, next := getGamesPage(t, srv.URL, "limit=1&cursor="+url.QueryEscape(*cursor))
		rest = append(rest, ids...)
		cursor = next
	}

	want := []string{testGamePublishedThird, testGamePublishedOlder}
	if len(rest) != len(want) {
		t.Fatalf("the rest of the scroll = %v, want %v", rest, want)
	}
	for i := range want {
		if rest[i] != want[i] {
			t.Errorf("game %d after the insert = %s, want %s (full order %v)",
				i, rest[i], want[i], rest)
		}
	}
}

// TestHandleGamesListPagingComposesWithFilters pages a filtered list: the
// cursor narrows the same set the filter did, rather than walking the library
// and filtering a page at a time, which would return short pages and a cursor
// that outran its own results.
func TestHandleGamesListPagingComposesWithFilters(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedFilterFixture(t, testContext(t), pool)

	for name, tc := range map[string]struct {
		query string
		want  []string
	}{
		"category": {
			"category=" + testCategoryIcebreaker,
			[]string{testGamePublishedNewer, testGamePublishedOlder},
		},
		"location": {
			"location=" + testLocationIndoor,
			[]string{testGamePublishedNewer, testGamePublishedThird},
		},
		"scalar": {
			"participants=8",
			[]string{testGamePublishedNewer, testGamePublishedThird, testGamePublishedOlder},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := pageGames(t, srv.URL, tc.query, 1)

			if len(got) != len(tc.want) {
				t.Fatalf("paged %d games %v, want %d %v",
					len(got), got, len(tc.want), tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("game %d = %s, want %s (full order %v)",
						i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

// TestHandleGamesListLastPageCarriesNoCursor pins the end of the scroll. The
// client stops on an absent cursor, so a full last page must still clear it
// rather than hand back one that answers an empty list.
func TestHandleGamesListLastPageCarriesNoCursor(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedFilterFixture(t, testContext(t), pool)

	for name, query := range map[string]string{
		"exactly one page":  "limit=3",
		"room to spare":     "limit=10",
		"default page size": "",
		"nothing matches":   "participants=100",
	} {
		t.Run(name, func(t *testing.T) {
			_, cursor := getGamesPage(t, srv.URL, query)
			if cursor != nil {
				t.Errorf("next_cursor = %q, want it absent", *cursor)
			}
		})
	}
}

// TestHandleGamesListDefaultLimitCapsThePage guards the default: a client that
// names no limit gets a page rather than the library.
func TestHandleGamesListDefaultLimitCapsThePage(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	ctx := testContext(t)

	insertAuthor(t, ctx, pool, testAuthorID)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	total := int(store.DefaultGameLimit) + 5
	for i := range total {
		insertGame(t, ctx, pool, testGame{
			// The suffix keeps the ids apart and ordered like the timestamps.
			ID:              fmt.Sprintf("60000000-0000-7000-8000-0000000%05d", i),
			Title:           fmt.Sprintf("Game %d", i),
			MinParticipants: ptr(int32(4)),
			MaxParticipants: ptr(int32(12)),
			DurationMin:     ptr(int32(10)),
			DurationMax:     ptr(int32(20)),
			PublishState:    "published",
			CreatedAt:       base.Add(time.Duration(i) * time.Minute),
		})
	}

	ids, cursor := getGamesPage(t, srv.URL, "")
	if len(ids) != int(store.DefaultGameLimit) {
		t.Fatalf("got %d games, want the default %d", len(ids), store.DefaultGameLimit)
	}
	if cursor == nil {
		t.Fatal("next_cursor is absent, but games remain")
	}

	if paged := pageGames(t, srv.URL, "", int(store.MaxGameLimit)); len(paged) != total {
		t.Errorf("paged %d games, want all %d", len(paged), total)
	}
}

// TestHandleGamesListRejectsMalformedPaging keeps the two paging parameters to
// the vocabulary the endpoint issues. A limit past the ceiling is refused
// rather than clamped, and a cursor that does not decode is refused rather than
// read as the first page, which would repeat rows the client already showed.
func TestHandleGamesListRejectsMalformedPaging(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedFilterFixture(t, testContext(t), pool)

	valid := base64.RawURLEncoding.EncodeToString(
		[]byte("2026-01-01T12:00:00Z|" + testGamePublishedNewer))

	for name, query := range map[string]string{
		"zero limit":            "limit=0",
		"negative limit":        "limit=-1",
		"limit past the cap":    fmt.Sprintf("limit=%d", store.MaxGameLimit+1),
		"limit not a number":    "limit=abc",
		"empty limit":           "limit=",
		"repeated limit":        "limit=1&limit=2",
		"cursor not base64":     "cursor=not!base64",
		"cursor without the id": base64Query("2026-01-01T12:00:00Z"),
		"cursor with a bad time": base64Query(
			"the first of january|" + testGamePublishedNewer),
		"cursor with a bad id": base64Query("2026-01-01T12:00:00Z|nonsense"),
		"empty cursor":         "cursor=",
		"repeated cursor":      "cursor=" + valid + "&cursor=" + valid,
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

			field := strings.SplitN(query, "=", 2)[0]
			if !strings.HasPrefix(got["error"], field+": ") {
				t.Errorf("error = %q, want it to start with %q", got["error"], field+": ")
			}
		})
	}
}

// base64Query wraps a cursor payload the way the endpoint encodes one, so the
// rejection tests exercise the decoded halves rather than the base64 alone.
func base64Query(payload string) string {
	return "cursor=" + base64.RawURLEncoding.EncodeToString([]byte(payload))
}

// TestHandleGamesListAcceptsItsOwnCursorVerbatim pins the round trip. The
// client hands back the string it was given, so the encoding has to survive a
// querystring without escaping.
func TestHandleGamesListAcceptsItsOwnCursorVerbatim(t *testing.T) {
	srv, pool := newAuthedTestServer(t)
	seedFilterFixture(t, testContext(t), pool)

	_, cursor := getGamesPage(t, srv.URL, "limit=1")
	if cursor == nil {
		t.Fatal("the first of three pages carried no cursor")
	}
	if escaped := url.QueryEscape(*cursor); escaped != *cursor {
		t.Errorf("cursor %q needs escaping as %q", *cursor, escaped)
	}

	ids, _ := getGamesPage(t, srv.URL, "limit=1&cursor="+*cursor)

	if len(ids) != 1 || ids[0] != testGamePublishedThird {
		t.Errorf("second page = %v, want [%s]", ids, testGamePublishedThird)
	}
}
