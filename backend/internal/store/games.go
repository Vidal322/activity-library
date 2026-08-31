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

// GameCategory is one category a game carries, with its family inlined so the
// client can group the list without a second request.
type GameCategory struct {
	ID           string `db:"id"`
	Name         string `db:"name"`
	Description  string `db:"description"`
	Active       bool   `db:"active"`
	DisplayOrder int32  `db:"display_order"`
	FamilyID     string `db:"family_id"`
	FamilyName   string `db:"family_name"`
}

// Location is one place a game can be played.
type Location struct {
	ID   string `db:"id"`
	Name string `db:"name"`
}

type GameMaterial struct {
	ID                     string `db:"id"`
	Name                   string `db:"name"`
	Description            string `db:"description"`
	QuantityBase           int32  `db:"quantity_base"`
	QuantityPerParticipant int32  `db:"quantity_per_participant"`
	Optional               bool   `db:"optional"`
}

type GameDetail struct {
	Game
	Categories []GameCategory
	Locations  []Location
	Materials  []GameMaterial
}

const getGameQuery = `
	SELECT id, title, description, image,
	       min_participants, max_participants,
	       duration_min, duration_max,
	       no_materials
	FROM games
	WHERE id = $1`

const getGameCategoriesQuery = `
	SELECT c.id, c.name, c.description, c.active, c.display_order,
	       f.id AS family_id, f.name AS family_name
	FROM game_categories gc
	JOIN categories c ON c.id = gc.category_id
	JOIN category_families f ON f.id = c.family_id
	WHERE gc.game_id = $1
	ORDER BY f.display_order, f.name, c.display_order, c.name`

const getGameLocationsQuery = `
	SELECT l.id, l.name
	FROM game_locations gl
	JOIN locations l ON l.id = gl.location_id
	WHERE gl.game_id = $1
	ORDER BY l.name`

const getGameMaterialRequirementsQuery = `
	SELECT m.id, m.name, m.description,
	       gm.quantity_base, gm.quantity_per_participant, gm.optional
	FROM game_materials gm
	JOIN materials m ON m.id = gm.material_id
	WHERE gm.game_id = $1
	ORDER BY m.name`

// GetGameByID returns one game with its categories, locations and materials.
func GetGameByID(ctx context.Context, pool *pgxpool.Pool, gameId string) (GameDetail, error) {
	row, err := pool.Query(ctx, getGameQuery, gameId)
	if err != nil {
		return GameDetail{}, classify(err)
	}

	game, err := pgx.CollectExactlyOneRow(row, pgx.RowToStructByName[Game])
	if err != nil {
		return GameDetail{}, classify(err)
	}

	categories, err := collectByGame[GameCategory](ctx, pool, getGameCategoriesQuery, gameId)
	if err != nil {
		return GameDetail{}, err
	}

	locations, err := collectByGame[Location](ctx, pool, getGameLocationsQuery, gameId)
	if err != nil {
		return GameDetail{}, err
	}

	materials, err := collectByGame[GameMaterial](
		ctx,
		pool,
		getGameMaterialRequirementsQuery,
		gameId,
	)
	if err != nil {
		return GameDetail{}, err
	}

	return GameDetail{
		Game:       game,
		Categories: categories,
		Locations:  locations,
		Materials:  materials,
	}, nil
}

func collectByGame[T any](
	ctx context.Context,
	pool *pgxpool.Pool,
	query, gameId string,
) ([]T, error) {
	rows, err := pool.Query(ctx, query, gameId)
	if err != nil {
		return nil, classify(err)
	}

	values, err := pgx.CollectRows(rows, pgx.RowToStructByName[T])
	if err != nil {
		return nil, classify(err)
	}

	if values == nil {
		values = []T{}
	}

	return values, nil
}
