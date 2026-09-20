package store

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Vidal322/activity-library/internal/optional"
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

	// PublishState is 'draft' or 'published'. It rides along with the card
	// because a draft only ever reaches its own author, who has to be able to
	// tell it apart from work they have already published.
	PublishState string `db:"publish_state"`

	// CreatedAt is the first half of the list's sort key, and so of the
	// cursor. It is not part of the card the client renders.
	CreatedAt time.Time `db:"created_at"`
	// Field not in db, the tag warns pgx about it
	Authors []GameAuthor `db:"-"`
}

type GameAuthor struct {
	ID   string  `db:"id"`
	Name string  `db:"name"`
	Img  *string `db:"img"`
}

type GameFilter struct {
	CategoryIDs  []string
	LocationIDs  []string
	Participants *int32
	Duration     *int32
	NoMaterials  *bool
}

// gameColumns, gameFilters and gameVisibility are shared with SearchGames in
// search.go, which differs from the list only in how it orders rows and
// positions a page.
const gameColumns = `
	SELECT g.id, g.title, g.description, g.image,
	       g.min_participants, g.max_participants,
	       g.duration_min, g.duration_max,
	       g.no_materials, g.publish_state, g.created_at`

const gameFilters = `
	  AND (
	        SELECT count(*) FROM game_categories gc
	        WHERE gc.game_id = g.id AND gc.category_id = ANY(@category_ids::uuid[])
	      ) = coalesce(cardinality(@category_ids::uuid[]), 0)
	  AND (
	        SELECT count(*) FROM game_locations gl
	        WHERE gl.game_id = g.id AND gl.location_id = ANY(@location_ids::uuid[])
	      ) = coalesce(cardinality(@location_ids::uuid[]), 0)
	  AND (@participants::int IS NULL OR (
	            (g.min_participants IS NULL OR g.min_participants <= @participants)
	        AND (g.max_participants IS NULL OR g.max_participants >= @participants)))
	  AND (@duration::int IS NULL OR (
	            (g.duration_min IS NULL OR g.duration_min <= @duration)
	        AND (g.duration_max IS NULL OR g.duration_max >= @duration)))
	  AND (@no_materials::bool IS NULL OR g.no_materials = @no_materials)`

func (f GameFilter) namedArgs() pgx.StrictNamedArgs {
	return pgx.StrictNamedArgs{
		"category_ids": f.CategoryIDs,
		"location_ids": f.LocationIDs,
		"participants": f.Participants,
		"duration":     f.Duration,
		"no_materials": f.NoMaterials,
	}
}

// gameAuthorship holds of a game the caller wrote. Seeing a game and changing
// one are different questions, so the two predicates below stay separate: a
// read admits more rows than a write does.
const gameAuthorship = `EXISTS (
	         SELECT 1 FROM game_authors ga
	         WHERE ga.game_id = g.id AND ga.user_id = @viewer_id::uuid)`

// gameVisibility admits a game the caller may see: published, or a draft the
// caller authored. Every query that carries it names the viewer the same way,
// so the predicate is the same text wherever it lands.
const gameVisibility = `
	  (g.publish_state = 'published'
	   OR ` + gameAuthorship + `)`

const listGamesQuery = gameColumns + `
	FROM games g
	WHERE` + gameVisibility + gameFilters + `
	  AND (@cursor_created_at::timestamptz IS NULL
	       OR (g.created_at, g.id) < (@cursor_created_at::timestamptz, @cursor_id::uuid))
	ORDER BY g.created_at DESC, g.id DESC
	LIMIT @limit`

const gameAuthorsQuery = `
	SELECT ga.game_id, u.id, u.name, u.img
	FROM game_authors ga
	JOIN users u ON u.id = ga.user_id
	WHERE ga.game_id = ANY($1::uuid[])
	ORDER BY ga.game_id, u.name COLLATE "pt-PT-x-icu", u.id`

func authorsByGame(
	ctx context.Context,
	pool *pgxpool.Pool,
	gameIDs []string,
) (map[string][]GameAuthor, error) {
	byGame := make(map[string][]GameAuthor, len(gameIDs))
	for _, id := range gameIDs {
		byGame[id] = []GameAuthor{}
	}

	if len(gameIDs) == 0 {
		return byGame, nil
	}

	rows, err := pool.Query(ctx, gameAuthorsQuery, gameIDs)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()

	for rows.Next() {
		var gameID string
		var a GameAuthor
		if err := rows.Scan(&gameID, &a.ID, &a.Name, &a.Img); err != nil {
			return nil, classify(err)
		}
		byGame[gameID] = append(byGame[gameID], a)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err)
	}

	return byGame, nil
}

func attachAuthors(ctx context.Context, pool *pgxpool.Pool, games []Game) error {
	if len(games) == 0 {
		return nil
	}

	ids := make([]string, len(games))
	for i, g := range games {
		ids[i] = g.ID
	}

	byGame, err := authorsByGame(ctx, pool, ids)
	if err != nil {
		return err
	}

	for i := range games {
		games[i].Authors = byGame[games[i].ID]
	}

	return nil
}

// GameCursor is the position of the last row a page returned
type GameCursor struct {
	CreatedAt time.Time
	ID        string
}

const (
	// DefaultGameLimit is the page size for a request that asks for none.
	DefaultGameLimit int32 = 20

	MaxGameLimit int32 = 100
)

// GamePage is where to start and how much to read. The zero value is the first
// page at the default size.
type GamePage struct {
	Limit  int32
	Cursor *GameCursor
}

// GameList is one page. Next is nil on the last page
type GameList struct {
	Games []Game
	Next  *GameCursor
}

// ListGames returns one page of the published games the filter admits, newest
// first. Drafts are invisible to this query.
func ListGames(
	ctx context.Context,
	pool *pgxpool.Pool,
	filter GameFilter,
	page GamePage,
	userID string,
) (GameList, error) {
	if err := checkFilterIDs(ctx, pool, filter); err != nil {
		return GameList{}, err
	}

	limit := page.Limit
	if limit <= 0 {
		limit = DefaultGameLimit
	}
	if limit > MaxGameLimit {
		limit = MaxGameLimit
	}

	var cursorCreatedAt *time.Time
	var cursorID *string
	if page.Cursor != nil {
		cursorCreatedAt = &page.Cursor.CreatedAt
		cursorID = &page.Cursor.ID
	}

	// One row past the page, and dropped below: it answers whether a further
	// page exists without a second count of the whole filtered set.
	args := filter.namedArgs()
	args["cursor_created_at"] = cursorCreatedAt
	args["cursor_id"] = cursorID
	args["limit"] = limit + 1
	args["viewer_id"] = userID

	rows, err := pool.Query(ctx, listGamesQuery, args)
	if err != nil {
		return GameList{}, classify(err)
	}

	games, err := pgx.CollectRows(rows, pgx.RowToStructByName[Game])
	if err != nil {
		return GameList{}, classify(err)
	}

	var next *GameCursor
	if int32(len(games)) > limit {
		games = games[:limit]
		last := games[len(games)-1]
		next = &GameCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}

	// After the page is trimmed, so the probe row past the end costs nothing.
	if err := attachAuthors(ctx, pool, games); err != nil {
		return GameList{}, err
	}

	return GameList{Games: games, Next: next}, nil
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
	SELECT g.id, g.title, g.description, g.image,
	       g.min_participants, g.max_participants,
	       g.duration_min, g.duration_max,
	       g.no_materials, g.publish_state, g.created_at
	FROM games g
	WHERE g.id = @game_id
	  AND` + gameVisibility

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
func GetGameByID(
	ctx context.Context,
	pool *pgxpool.Pool,
	gameId string,
	userId string,
) (GameDetail, error) {
	row, err := pool.Query(ctx, getGameQuery, pgx.StrictNamedArgs{
		"game_id":   gameId,
		"viewer_id": userId,
	})
	if err != nil {
		return GameDetail{}, classify(err)
	}

	game, err := pgx.CollectExactlyOneRow(row, pgx.RowToStructByName[Game])
	if err != nil {
		return GameDetail{}, classify(err)
	}

	byGame, err := authorsByGame(ctx, pool, []string{game.ID})
	if err != nil {
		return GameDetail{}, err
	}
	game.Authors = byGame[game.ID]

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

type Draft struct {
	Title           string
	Description     string
	Image           *string
	MinParticipants *int32
	MaxParticipants *int32
	DurationMin     *int32
	DurationMax     *int32
	NoMaterials     bool
}

const createDraftQuery = `
	INSERT INTO games (
		title, description, image,
		min_participants, max_participants, duration_min, duration_max,
		no_materials, publish_state)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'draft')
	RETURNING id, title, description, image,
	          min_participants, max_participants,
	          duration_min, duration_max,
	          no_materials, publish_state, created_at`

const createDraftAuthorQuery = `
	INSERT INTO game_authors (game_id, user_id) VALUES ($1, $2)`

func CreateDraft(
	ctx context.Context,
	pool *pgxpool.Pool,
	draft Draft,
	authorID string,
) (GameDetail, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return GameDetail{}, classify(err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, createDraftQuery,
		draft.Title, draft.Description, draft.Image,
		draft.MinParticipants, draft.MaxParticipants,
		draft.DurationMin, draft.DurationMax,
		draft.NoMaterials)
	if err != nil {
		return GameDetail{}, classify(err)
	}

	game, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[Game])
	if err != nil {
		return GameDetail{}, classify(err)
	}

	if _, err := tx.Exec(ctx, createDraftAuthorQuery, game.ID, authorID); err != nil {
		return GameDetail{}, classify(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return GameDetail{}, classify(err)
	}

	byGame, err := authorsByGame(ctx, pool, []string{game.ID})
	if err != nil {
		return GameDetail{}, err
	}
	game.Authors = byGame[game.ID]

	return GameDetail{
		Game:       game,
		Categories: []GameCategory{},
		Locations:  []Location{},
		Materials:  []GameMaterial{},
		Blocks:     []GameBlock{},
	}, nil
}

// Optional used to distinguish absent from nul
type GameEdit struct {
	Title           optional.Value[string]
	Description     optional.Value[string]
	Image           optional.Value[string]
	MinParticipants optional.Value[int32]
	MaxParticipants optional.Value[int32]
	DurationMin     optional.Value[int32]
	DurationMax     optional.Value[int32]
	NoMaterials     optional.Value[bool]
}

type assignable interface {
	IsSet() bool
	Arg() any
}

// assignments returns one SET clause per named field, with the arguments to
// match. Only the named fields appear: a column nobody mentioned is a column
// the UPDATE never touches, which is what makes absent different from null.
func (e GameEdit) assignments() ([]string, pgx.StrictNamedArgs) {
	fields := []struct {
		column string
		value  assignable
	}{
		{"title", e.Title},
		{"description", e.Description},
		{"image", e.Image},
		{"min_participants", e.MinParticipants},
		{"max_participants", e.MaxParticipants},
		{"duration_min", e.DurationMin},
		{"duration_max", e.DurationMax},
		{"no_materials", e.NoMaterials},
	}

	sets := make([]string, 0, len(fields))
	args := make(pgx.StrictNamedArgs, len(fields))

	for _, f := range fields {
		if !f.value.IsSet() {
			continue
		}

		// The column names its own argument, so a field that stays absent
		// leaves no trace in either the clause or the arguments.
		args[f.column] = f.value.Arg()
		sets = append(sets, f.column+" = @"+f.column)
	}

	return sets, args
}

func EditGame(
	ctx context.Context,
	pool *pgxpool.Pool,
	gameID string,
	edit GameEdit,
	userID string,
) (GameDetail, error) {
	sets, args := edit.assignments()

	if len(sets) == 0 {
		return GetGameByID(ctx, pool, gameID, userID)
	}

	args["game_id"] = gameID
	args["viewer_id"] = userID

	query := `
	UPDATE games AS g
	SET ` + strings.Join(sets, ", ") + `
	WHERE g.id = @game_id
	  AND ` + gameAuthorship

	tag, err := pool.Exec(ctx, query, args)
	if err != nil {
		return GameDetail{}, classify(err)
	}

	if tag.RowsAffected() == 0 {
		return GameDetail{}, ErrNotFound
	}

	return GetGameByID(ctx, pool, gameID, userID)
}

const gameAuthorQuery = `
	SELECT ` + gameAuthorship + `
	FROM games g
	WHERE g.id = @game_id
	  AND` + gameVisibility

// rowQuerier is the one method the authorship check needs, so the check can
// run against the pool on its own or inside a transaction that is about to
// write.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func AuthorizeGameWrite(
	ctx context.Context,
	pool *pgxpool.Pool,
	gameID string,
	userID string,
) error {
	return authorizeGameWrite(ctx, pool, gameID, userID)
}

func authorizeGameWrite(
	ctx context.Context,
	q rowQuerier,
	gameID string,
	userID string,
) error {
	var authored bool

	err := q.QueryRow(ctx, gameAuthorQuery, pgx.StrictNamedArgs{
		"game_id":   gameID,
		"viewer_id": userID,
	}).Scan(&authored)
	if err != nil {
		return classify(err)
	}

	if !authored {
		return ErrForbidden
	}

	return nil
}

type GameBlockInput struct {
	ID      string
	Type    string
	Content string
}

// UnknownBlockError names a block the request claimed to be editing that this
// game does not have. It unwraps to ErrInvalid: the game is there and the
// caller may write it, so the request is unprocessable rather than missing.
type UnknownBlockError struct {
	ID string
}

func (e *UnknownBlockError) Error() string {
	return "unknown block: " + e.ID
}

func (e *UnknownBlockError) Unwrap() error {
	return ErrInvalid
}

const (
	deleteRemovedBlocksQuery = `
		DELETE FROM blocks
		WHERE game_id = @game_id
		  AND NOT (id = ANY (@keep::uuid[]))`

	updateBlockQuery = `
		UPDATE blocks
		SET type = @type, content = @content, position = @position
		WHERE id = @id AND game_id = @game_id`

	insertBlockQuery = `
		INSERT INTO blocks (game_id, type, content, position)
		VALUES (@game_id, @type, @content, @position)`
)

func EditGameBlocks(
	ctx context.Context,
	pool *pgxpool.Pool,
	gameID string,
	blocks []GameBlockInput,
	userID string,
) (GameDetail, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return GameDetail{}, classify(err)
	}
	defer tx.Rollback(ctx)

	if err := authorizeGameWrite(ctx, tx, gameID, userID); err != nil {
		return GameDetail{}, err
	}

	keep := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.ID != "" {
			keep = append(keep, b.ID)
		}
	}

	_, err = tx.Exec(ctx, deleteRemovedBlocksQuery, pgx.StrictNamedArgs{
		"game_id": gameID,
		"keep":    keep,
	})
	if err != nil {
		return GameDetail{}, classify(err)
	}

	for i, b := range blocks {
		position := int32(i)

		if b.ID == "" {
			_, err = tx.Exec(ctx, insertBlockQuery, pgx.StrictNamedArgs{
				"game_id":  gameID,
				"type":     b.Type,
				"content":  b.Content,
				"position": position,
			})
			if err != nil {
				return GameDetail{}, classify(err)
			}
			continue
		}

		tag, err := tx.Exec(ctx, updateBlockQuery, pgx.StrictNamedArgs{
			"id":       b.ID,
			"game_id":  gameID,
			"type":     b.Type,
			"content":  b.Content,
			"position": position,
		})
		if err != nil {
			return GameDetail{}, classify(err)
		}

		if tag.RowsAffected() == 0 {
			return GameDetail{}, &UnknownBlockError{ID: b.ID}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return GameDetail{}, classify(err)
	}

	return GetGameByID(ctx, pool, gameID, userID)
}
