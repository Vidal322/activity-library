package store

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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

// GameFilter narrows the list. Ids within a field AND together, and the fields
// AND with each other: a game has to carry every category and every location
// asked for, and fit the participant count and duration on top. Participants
// and Duration are the counsellor's own situation, not a range to overlap: a
// game matches when its declared bounds contain the value. The zero value
// filters nothing, and a nil scalar is one filter left off rather than a value
// of zero or false.
type GameFilter struct {
	CategoryIDs  []string
	LocationIDs  []string
	Participants *int32
	Duration     *int32
	NoMaterials  *bool
}

const listGamesQuery = `
	SELECT g.id, g.title, g.description, g.image,
	       g.min_participants, g.max_participants,
	       g.duration_min, g.duration_max,
	       g.no_materials
	FROM games g
	WHERE g.publish_state = 'published'
	  AND (
	        SELECT count(*) FROM game_categories gc
	        WHERE gc.game_id = g.id AND gc.category_id = ANY($1::uuid[])
	      ) = coalesce(cardinality($1::uuid[]), 0)
	  AND (
	        SELECT count(*) FROM game_locations gl
	        WHERE gl.game_id = g.id AND gl.location_id = ANY($2::uuid[])
	      ) = coalesce(cardinality($2::uuid[]), 0)
	  AND ($3::int IS NULL OR (
	            (g.min_participants IS NULL OR g.min_participants <= $3)
	        AND (g.max_participants IS NULL OR g.max_participants >= $3)))
	  AND ($4::int IS NULL OR (
	            (g.duration_min IS NULL OR g.duration_min <= $4)
	        AND (g.duration_max IS NULL OR g.duration_max >= $4)))
	  AND ($5::bool IS NULL OR g.no_materials = $5)
	ORDER BY g.created_at DESC, g.id DESC`

// ListGames returns every published game the filter admits. Drafts are
// invisible to this query.
func ListGames(ctx context.Context, pool *pgxpool.Pool, filter GameFilter) ([]Game, error) {
	if err := checkFilterIDs(ctx, pool, filter); err != nil {
		return nil, err
	}

	rows, err := pool.Query(
		ctx,
		listGamesQuery,
		filter.CategoryIDs,
		filter.LocationIDs,
		filter.Participants,
		filter.Duration,
		filter.NoMaterials,
	)
	if err != nil {
		return nil, classify(err)
	}

	games, err := pgx.CollectRows(rows, pgx.RowToStructByName[Game])
	if err != nil {
		return nil, classify(err)
	}

	return games, nil
}

// UnknownFilterError reports filter ids that name no row.
// parameter that carried them
type UnknownFilterError struct {
	Field string
	IDs   []string
}

func (e *UnknownFilterError) Error() string {
	return "unknown " + e.Field + ": " + strings.Join(e.IDs, ", ")
}

const (
	unknownCategoryIDsQuery = `
		SELECT id FROM unnest($1::uuid[]) AS id
		EXCEPT
		SELECT id FROM categories`

	unknownLocationIDsQuery = `
		SELECT id FROM unnest($1::uuid[]) AS id
		EXCEPT
		SELECT id FROM locations`
)

// checkFilterIDs rejects the whole filter if any id is absent. It costs one
// round trip per populated field, and none at all for an unfiltered list.
func checkFilterIDs(ctx context.Context, pool *pgxpool.Pool, filter GameFilter) error {
	if len(filter.CategoryIDs) > 0 {
		missing, err := unknownIDs(ctx, pool, unknownCategoryIDsQuery, filter.CategoryIDs)
		if err != nil {
			return err
		}
		if len(missing) > 0 {
			return &UnknownFilterError{Field: "category", IDs: missing}
		}
	}

	if len(filter.LocationIDs) > 0 {
		missing, err := unknownIDs(ctx, pool, unknownLocationIDsQuery, filter.LocationIDs)
		if err != nil {
			return err
		}
		if len(missing) > 0 {
			return &UnknownFilterError{Field: "location", IDs: missing}
		}
	}

	return nil
}

func unknownIDs(
	ctx context.Context,
	pool *pgxpool.Pool,
	query string,
	ids []string,
) ([]string, error) {
	rows, err := pool.Query(ctx, query, ids)
	if err != nil {
		return nil, classify(err)
	}

	// Scanned as uuid rather than string: the column is a uuid, and pgtype
	// renders it in the canonical hyphenated form whatever spelling the client
	// sent.
	missing, err := pgx.CollectRows(rows, pgx.RowTo[pgtype.UUID])
	if err != nil {
		return nil, classify(err)
	}

	out := make([]string, 0, len(missing))
	for _, id := range missing {
		out = append(out, id.String())
	}

	return out, nil
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

type GameBlock struct {
	ID       string `db:"id"`
	Type     string `db:"type"`
	Content  string `db:"content"`
	Position int32  `db:"position"`
}

type GameDetail struct {
	Game
	Categories []GameCategory
	Locations  []Location
	Materials  []GameMaterial
	Blocks     []GameBlock
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

const getGameBlocksQuery = `
	SELECT b.id, b.type, b.content, b.position
	FROM blocks b
	WHERE b.game_id = $1
	ORDER BY b.position`

// GetGameByID returns one game with its categories, locations, materials and
// blocks.
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

	blocks, err := collectByGame[GameBlock](ctx, pool, getGameBlocksQuery, gameId)
	if err != nil {
		return GameDetail{}, err
	}

	return GameDetail{
		Game:       game,
		Categories: categories,
		Locations:  locations,
		Materials:  materials,
		Blocks:     blocks,
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
