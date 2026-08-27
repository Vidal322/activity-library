package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Game struct {
	ID              string  `db:"id"`
	Title           string  `db:"title"`
	Description     string  `db:"description"`
	Image           *string `db:"image"`
	MinParticipants *int32  `db:"min_participants"`
	MaxParticipants *int32  `db:"max_participants"`
	DurationMin     *int32  `db:"duration_min"`
	DurationMax     *int32  `db:"duration_max"`
	NoMaterials     bool    `db:"no_materials"`
}

const listGamesQuery = `
	SELECT id, title, description, image,
	       min_participants, max_participants,
	       duration_min, duration_max,
	       no_materials
	FROM games
	WHERE publish_state = 'published'
	ORDER BY created_at DESC, id DESC`

// ListGames returns every published game. Drafts are invisible to this query
func ListGames(ctx context.Context, pool *pgxpool.Pool) ([]Game, error) {
	rows, err := pool.Query(ctx, listGamesQuery)
	if err != nil {
		return nil, classify(err)
	}

	// CollectRows closes rows and surfaces any error the scan or the server
	// raised after the first row.
	games, err := pgx.CollectRows(rows, pgx.RowToStructByName[Game])
	if err != nil {
		return nil, classify(err)
	}

	return games, nil
}
