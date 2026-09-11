package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// searchGamesQuery ranks with ts_rank_cd
const searchGamesQuery = gameColumns + `
	FROM games g
	JOIN game_search gs ON gs.game_id = g.id
	WHERE g.publish_state = 'published'` + gameFilters + `
	  AND gs.document @@ websearch_to_tsquery('pt_unaccent', $6)
	ORDER BY ts_rank_cd(gs.document, websearch_to_tsquery('pt_unaccent', $6)) DESC,
	         g.created_at DESC, g.id DESC
	LIMIT $7 OFFSET $8`

// MaxGameSearchOffset caps how deep a search can be paged.
const MaxGameSearchOffset int32 = 1000

// GameSearch is one page of ranked results. NextOffset is nil on the last
// page, and is where the following page starts rather than a cursor: encoding
// it for the client is the API layer's business, as it already is for the
// list.
type GameSearch struct {
	Games      []Game
	NextOffset *int32
}

func SearchGames(
	ctx context.Context,
	pool *pgxpool.Pool,
	q string,
	filter GameFilter,
	limit int32,
	offset int32,
) (GameSearch, error) {
	if err := checkFilterIDs(ctx, pool, filter); err != nil {
		return GameSearch{}, err
	}

	if limit <= 0 {
		limit = DefaultGameLimit
	}
	if limit > MaxGameLimit {
		limit = MaxGameLimit
	}
	if offset < 0 {
		offset = 0
	}

	rows, err := pool.Query(
		ctx,
		searchGamesQuery,
		filter.CategoryIDs,
		filter.LocationIDs,
		filter.Participants,
		filter.Duration,
		filter.NoMaterials,
		q,
		limit+1,
		offset,
	)
	if err != nil {
		return GameSearch{}, classify(err)
	}

	games, err := pgx.CollectRows(rows, pgx.RowToStructByName[Game])
	if err != nil {
		return GameSearch{}, classify(err)
	}

	var next *int32
	if int32(len(games)) > limit {
		games = games[:limit]
		if following := offset + limit; following <= MaxGameSearchOffset {
			next = &following
		}
	}

	// As in ListGames: after the trim, so the probe row past the end costs
	// nothing.
	if err := attachAuthors(ctx, pool, games); err != nil {
		return GameSearch{}, err
	}

	return GameSearch{Games: games, NextOffset: next}, nil
}
