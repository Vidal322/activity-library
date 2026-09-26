package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Vidal322/activity-library/internal/optional"
	"github.com/Vidal322/activity-library/internal/store"
)

type gameAuthor struct {
	ID   string  `json:"id"`
	Name string  `json:"name"`
	Img  *string `json:"img"`
}

type gameSummary struct {
	ID              string       `json:"id"`
	Title           string       `json:"title"`
	Description     string       `json:"description"`
	Image           *string      `json:"image"`
	MinParticipants *int32       `json:"min_participants"`
	MaxParticipants *int32       `json:"max_participants"`
	DurationMin     *int32       `json:"duration_min"`
	DurationMax     *int32       `json:"duration_max"`
	NoMaterials     bool         `json:"no_materials"`
	PublishState    string       `json:"publish_state"`
	Authors         []gameAuthor `json:"authors"`
}

// gamesListResponse carries the page and the cursor that reaches the next one.
// NextCursor is null on the last page: the client pages until it is absent
// rather than until the list comes back shorter than the limit.
type gamesListResponse struct {
	Games      []gameSummary `json:"games"`
	NextCursor *string       `json:"next_cursor"`
}

func newGameSummary(g store.Game) gameSummary {
	authors := make([]gameAuthor, 0, len(g.Authors))
	for _, a := range g.Authors {
		authors = append(authors, gameAuthor{ID: a.ID, Name: a.Name, Img: a.Img})
	}

	return gameSummary{
		ID:              g.ID,
		Title:           g.Title,
		Description:     g.Description,
		Image:           g.Image,
		MinParticipants: g.MinParticipants,
		MaxParticipants: g.MaxParticipants,
		DurationMin:     g.DurationMin,
		DurationMax:     g.DurationMax,
		NoMaterials:     g.NoMaterials,
		PublishState:    g.PublishState,
		Authors:         authors,
	}
}

type gameCategory struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Active       bool   `json:"active"`
	DisplayOrder int32  `json:"display_order"`
	FamilyID     string `json:"family_id"`
	FamilyName   string `json:"family_name"`
}

type gameLocation struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type gameMaterial struct {
	ID                     string `json:"id"`
	Name                   string `json:"name"`
	Description            string `json:"description"`
	QuantityBase           int32  `json:"quantity_base"`
	QuantityPerParticipant int32  `json:"quantity_per_participant"`
	Optional               bool   `json:"optional"`
}

type gameBlock struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Content  string `json:"content"`
	Position int32  `json:"position"`
}

// gameDetail embeds the summary so a card and a detail page read the same
// spine fields, and adds the associations.
type gameDetail struct {
	gameSummary
	Categories []gameCategory `json:"categories"`
	Locations  []gameLocation `json:"locations"`
	Materials  []gameMaterial `json:"materials"`
	Blocks     []gameBlock    `json:"blocks"`
}

func newGameDetail(g store.GameDetail) gameDetail {
	categories := make([]gameCategory, 0, len(g.Categories))
	for _, c := range g.Categories {
		categories = append(categories, gameCategory{
			ID:           c.ID,
			Name:         c.Name,
			Description:  c.Description,
			Active:       c.Active,
			DisplayOrder: c.DisplayOrder,
			FamilyID:     c.FamilyID,
			FamilyName:   c.FamilyName,
		})
	}

	locations := make([]gameLocation, 0, len(g.Locations))
	for _, l := range g.Locations {
		locations = append(locations, gameLocation{ID: l.ID, Name: l.Name})
	}

	materials := make([]gameMaterial, 0, len(g.Materials))
	for _, m := range g.Materials {
		materials = append(materials, gameMaterial{
			ID:                     m.ID,
			Name:                   m.Name,
			Description:            m.Description,
			QuantityBase:           m.QuantityBase,
			QuantityPerParticipant: m.QuantityPerParticipant,
			Optional:               m.Optional,
		})
	}

	blocks := make([]gameBlock, 0, len(g.Blocks))
	for _, b := range g.Blocks {
		blocks = append(
			blocks,
			gameBlock{ID: b.ID, Type: b.Type, Content: b.Content, Position: b.Position},
		)
	}

	return gameDetail{
		gameSummary: newGameSummary(g.Game),
		Categories:  categories,
		Locations:   locations,
		Materials:   materials,
		Blocks:      blocks,
	}
}

// handleGamesList serves GET /v1/games. The category and location parameters
// repeat and AND together, so ?category=a&category=b asks for the games
// carrying both. participants, duration and no_materials take a single value
// each, and every filter given has to hold at once.
//
// query searches the full text of the game and narrows alongside the filters
// rather than replacing them, since the filter rail stays live while a search
// is running. It also changes the order: results come back best match first
// instead of newest first, and so are paged by counting rows rather than by
// the keyset the list walks. Both kinds of position travel in the one opaque
// cursor the response carries.
func (s *Server) handleGamesList(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	categoryIDs, err := parseIDParam(query["category"])
	if err != nil {
		s.writeBadRequest(w, "category: "+err.Error())
		return
	}

	locationIDs, err := parseIDParam(query["location"])
	if err != nil {
		s.writeBadRequest(w, "location: "+err.Error())
		return
	}

	participants, err := parseIntParam(query["participants"])
	if err != nil {
		s.writeBadRequest(w, "participants: "+err.Error())
		return
	}

	duration, err := parseIntParam(query["duration"])
	if err != nil {
		s.writeBadRequest(w, "duration: "+err.Error())
		return
	}

	noMaterials, err := parseBoolParam(query["no_materials"])
	if err != nil {
		s.writeBadRequest(w, "no_materials: "+err.Error())
		return
	}

	limit, err := parseLimitParam(query["limit"])
	if err != nil {
		s.writeBadRequest(w, "limit: "+err.Error())
		return
	}

	q, err := parseTextParam(query["query"])
	if err != nil {
		s.writeBadRequest(w, "query: "+err.Error())
		return
	}

	filter := store.GameFilter{
		CategoryIDs:  categoryIDs,
		LocationIDs:  locationIDs,
		Participants: participants,
		Duration:     duration,
		NoMaterials:  noMaterials,
	}

	var (
		games      []store.Game
		nextCursor *string
	)

	userID, _ := userIDFromContext(r.Context())
	if q != nil {
		offset, err := parseOffsetCursor(query["cursor"])
		if err != nil {
			s.writeBadRequest(w, "cursor: "+err.Error())
			return
		}

		found, err := store.SearchGames(r.Context(), s.pool, *q, filter, limit, offset, userID)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}

		games = found.Games
		if found.NextOffset != nil {
			next := encodeOffsetCursor(*found.NextOffset)
			nextCursor = &next
		}
	} else {
		cursor, err := parseCursorParam(query["cursor"])
		if err != nil {
			s.writeBadRequest(w, "cursor: "+err.Error())
			return
		}

		page, err := store.ListGames(r.Context(), s.pool, filter,
			store.GamePage{Limit: limit, Cursor: cursor}, userID)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}

		games = page.Games
		if page.Next != nil {
			next := encodeCursor(*page.Next)
			nextCursor = &next
		}
	}

	summaries := make([]gameSummary, 0, len(games))
	for _, g := range games {
		summaries = append(summaries, newGameSummary(g))
	}

	body := gamesListResponse{Games: summaries, NextCursor: nextCursor}

	if err := writeJSON(w, http.StatusOK, body); err != nil {
		slog.Error("Failed to write games list response", "error", err)
	}
}

// handleGetGame serves GET /v1/games/{id}.
func (s *Server) handleGetGame(w http.ResponseWriter, r *http.Request) {
	gameID := chi.URLParam(r, "id")

	var parsed pgtype.UUID
	if err := parsed.Scan(gameID); err != nil {
		s.writeBadRequest(w, "malformed game id")
		return
	}

	userID, _ := userIDFromContext(r.Context())

	game, err := store.GetGameByID(r.Context(), s.pool, gameID, userID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	if err := writeJSON(w, http.StatusOK, newGameDetail(game)); err != nil {
		slog.Error("Failed to write get game response", "error", err)
	}
}

type createDraftRequest struct {
	Title           string  `json:"title"`
	Description     string  `json:"description"`
	Image           *string `json:"image"`
	MinParticipants *int32  `json:"min_participants"`
	MaxParticipants *int32  `json:"max_participants"`
	DurationMin     *int32  `json:"duration_min"`
	DurationMax     *int32  `json:"duration_max"`
	NoMaterials     bool    `json:"no_materials"`
}

const maxTitle = 200

func (req createDraftRequest) validate() string {
	switch {
	case req.Title == "":
		return "title is required"

	case utf8.RuneCountInString(req.Title) > maxTitle:
		return fmt.Sprintf("title must not be longer than %d characters", maxTitle)
	}

	return ""
}

func (req createDraftRequest) draft() store.Draft {
	return store.Draft{
		Title:           req.Title,
		Description:     req.Description,
		Image:           req.Image,
		MinParticipants: req.MinParticipants,
		MaxParticipants: req.MaxParticipants,
		DurationMin:     req.DurationMin,
		DurationMax:     req.DurationMax,
		NoMaterials:     req.NoMaterials,
	}
}

func (s *Server) handleCreateDraft(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		s.writeUnauthorized(w, msgNotAuthenticated)
		return
	}

	var req createDraftRequest
	if err := readJSON(w, r, &req); err != nil {
		s.writeBadRequest(w, err.Error())
		return
	}

	req.Title = strings.TrimSpace(req.Title)
	req.Description = strings.TrimSpace(req.Description)

	if msg := req.validate(); msg != "" {
		s.writeUnprocessable(w, msg)
		return
	}

	game, err := store.CreateDraft(r.Context(), s.pool, req.draft(), userID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	w.Header().Set("Location", apiPrefix+"/games/"+game.ID)

	if err := writeJSON(w, http.StatusCreated, newGameDetail(game)); err != nil {
		slog.Error("Failed to write create draft response", "error", err)
	}
}

type editGameRequest struct {
	Title           optional.Value[string] `json:"title"`
	Description     optional.Value[string] `json:"description"`
	Image           optional.Value[string] `json:"image"`
	MinParticipants optional.Value[int32]  `json:"min_participants"`
	MaxParticipants optional.Value[int32]  `json:"max_participants"`
	DurationMin     optional.Value[int32]  `json:"duration_min"`
	DurationMax     optional.Value[int32]  `json:"duration_max"`
	NoMaterials     optional.Value[bool]   `json:"no_materials"`
}

func (req *editGameRequest) normalize() {
	if title, ok := req.Title.Get(); ok {
		req.Title = optional.Of(strings.TrimSpace(title))
	}
	if description, ok := req.Description.Get(); ok {
		req.Description = optional.Of(strings.TrimSpace(description))
	}
}

func (req editGameRequest) validate() string {
	switch {
	case req.Title.IsNull():
		return "title must not be null"

	case req.Description.IsNull():
		return "description must not be null"

	case req.NoMaterials.IsNull():
		return "no_materials must not be null"
	}

	if title, ok := req.Title.Get(); ok {
		switch {
		case title == "":
			return "title must not be empty"

		case utf8.RuneCountInString(title) > maxTitle:
			return fmt.Sprintf("title must not be longer than %d characters", maxTitle)
		}
	}

	return ""
}

func (req editGameRequest) edit() store.GameEdit {
	return store.GameEdit{
		Title:           req.Title,
		Description:     req.Description,
		Image:           req.Image,
		MinParticipants: req.MinParticipants,
		MaxParticipants: req.MaxParticipants,
		DurationMin:     req.DurationMin,
		DurationMax:     req.DurationMax,
		NoMaterials:     req.NoMaterials,
	}
}

// handleEditGame serves PATCH /v1/games/{id}, returning the game as it stands
// after the update.
func (s *Server) handleEditGame(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		s.writeUnauthorized(w, msgNotAuthenticated)
		return
	}

	gameID := chi.URLParam(r, "id")

	var parsed pgtype.UUID
	if err := parsed.Scan(gameID); err != nil {
		s.writeBadRequest(w, "malformed game id")
		return
	}

	err := store.AuthorizeGameWrite(r.Context(), s.pool, gameID, userID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	var req editGameRequest
	if err := readJSON(w, r, &req); err != nil {
		s.writeBadRequest(w, err.Error())
		return
	}

	req.normalize()

	if msg := req.validate(); msg != "" {
		s.writeUnprocessable(w, msg)
		return
	}

	game, err := store.EditGame(r.Context(), s.pool, gameID, req.edit(), userID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	if err := writeJSON(w, http.StatusOK, newGameDetail(game)); err != nil {
		slog.Error("Failed to write edit game response", "error", err)
	}
}

var blockTypes = []string{
	"paragraph", "steps", "bullet_points",
	"table", "heading", "note", "image",
}

type blockInput struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

type editGameBlocksRequest struct {
	Blocks *[]blockInput `json:"blocks"`
}

func (req editGameBlocksRequest) validate() string {
	if req.Blocks == nil {
		return "blocks must be an array"
	}

	seen := make(map[string]bool, len(*req.Blocks))

	for i, b := range *req.Blocks {
		if !slices.Contains(blockTypes, b.Type) {
			return fmt.Sprintf("blocks[%d].type must be one of %s",
				i, strings.Join(blockTypes, ", "))
		}

		if b.ID == "" {
			continue
		}

		var parsed pgtype.UUID
		if err := parsed.Scan(b.ID); err != nil {
			return fmt.Sprintf("blocks[%d].id is malformed", i)
		}

		if seen[b.ID] {
			return fmt.Sprintf("blocks[%d].id appears more than once", i)
		}
		seen[b.ID] = true
	}

	return ""
}

func (req editGameBlocksRequest) blocks() []store.GameBlockInput {
	blocks := make([]store.GameBlockInput, 0, len(*req.Blocks))
	for _, b := range *req.Blocks {
		blocks = append(
			blocks,
			store.GameBlockInput{ID: b.ID, Type: b.Type, Content: b.Content},
		)
	}

	return blocks
}

func (s *Server) handleEditGameBlocks(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		s.writeUnauthorized(w, msgNotAuthenticated)
		return
	}

	gameID := chi.URLParam(r, "id")

	var parsed pgtype.UUID
	if err := parsed.Scan(gameID); err != nil {
		s.writeBadRequest(w, "malformed game id")
		return
	}

	err := store.AuthorizeGameWrite(r.Context(), s.pool, gameID, userID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	var req editGameBlocksRequest
	if err := readJSON(w, r, &req); err != nil {
		s.writeBadRequest(w, err.Error())
		return
	}

	if msg := req.validate(); msg != "" {
		s.writeUnprocessable(w, msg)
		return
	}

	game, err := store.EditGameBlocks(r.Context(), s.pool, gameID, req.blocks(), userID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	if err := writeJSON(w, http.StatusOK, newGameDetail(game)); err != nil {
		slog.Error("Failed to write edit game blocks response", "error", err)
	}
}

type editGameCategoriesRequest struct {
	Categories *[]string `json:"categories"`
}

func (req editGameCategoriesRequest) validate() string {
	if req.Categories == nil {
		return "categories must be an array"
	}

	return validateIDSet(*req.Categories, "categories")
}

func validateIDSet(ids []string, field string) string {
	seen := make(map[string]bool, len(ids))

	for i, id := range ids {
		var parsed pgtype.UUID
		if err := parsed.Scan(id); err != nil {
			return fmt.Sprintf("%s[%d] is malformed", field, i)
		}

		if seen[id] {
			return fmt.Sprintf("%s[%d] appears more than once", field, i)
		}
		seen[id] = true
	}

	return ""
}

func (req editGameCategoriesRequest) categoryIDs() []string {
	ids := make([]string, 0, len(*req.Categories))

	return append(ids, *req.Categories...)
}

func (s *Server) handleEditGameCategories(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		s.writeUnauthorized(w, msgNotAuthenticated)
		return
	}

	gameID := chi.URLParam(r, "id")

	var parsed pgtype.UUID
	if err := parsed.Scan(gameID); err != nil {
		s.writeBadRequest(w, "malformed game id")
		return
	}

	err := store.AuthorizeGameWrite(r.Context(), s.pool, gameID, userID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	var req editGameCategoriesRequest
	if err := readJSON(w, r, &req); err != nil {
		s.writeBadRequest(w, err.Error())
		return
	}

	if msg := req.validate(); msg != "" {
		s.writeUnprocessable(w, msg)
		return
	}

	game, err := store.EditGameCategories(
		r.Context(), s.pool, gameID, req.categoryIDs(), userID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	if err := writeJSON(w, http.StatusOK, newGameDetail(game)); err != nil {
		slog.Error("Failed to write edit game categories response", "error", err)
	}
}

type editGameLocationsRequest struct {
	Locations *[]string `json:"locations"`
}

func (req editGameLocationsRequest) validate() string {
	if req.Locations == nil {
		return "locations must be an array"
	}

	return validateIDSet(*req.Locations, "locations")
}

func (req editGameLocationsRequest) locationIDs() []string {
	ids := make([]string, 0, len(*req.Locations))

	return append(ids, *req.Locations...)
}

func (s *Server) handleEditGameLocations(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		s.writeUnauthorized(w, msgNotAuthenticated)
		return
	}

	gameID := chi.URLParam(r, "id")

	var parsed pgtype.UUID
	if err := parsed.Scan(gameID); err != nil {
		s.writeBadRequest(w, "malformed game id")
		return
	}

	err := store.AuthorizeGameWrite(r.Context(), s.pool, gameID, userID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	var req editGameLocationsRequest
	if err := readJSON(w, r, &req); err != nil {
		s.writeBadRequest(w, err.Error())
		return
	}

	if msg := req.validate(); msg != "" {
		s.writeUnprocessable(w, msg)
		return
	}

	game, err := store.EditGameLocations(
		r.Context(), s.pool, gameID, req.locationIDs(), userID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	if err := writeJSON(w, http.StatusOK, newGameDetail(game)); err != nil {
		slog.Error("Failed to write edit game locations response", "error", err)
	}
}

type materialInput struct {
	ID                     string `json:"id"`
	QuantityBase           int32  `json:"quantity_base"`
	QuantityPerParticipant int32  `json:"quantity_per_participant"`
	Optional               bool   `json:"optional"`
}

type editGameMaterialsRequest struct {
	Materials *[]materialInput `json:"materials"`
}

func (req editGameMaterialsRequest) validate() string {
	if req.Materials == nil {
		return "materials must be an array"
	}

	seen := make(map[string]bool, len(*req.Materials))

	for i, m := range *req.Materials {
		var parsed pgtype.UUID
		if err := parsed.Scan(m.ID); err != nil {
			return fmt.Sprintf("materials[%d].id is malformed", i)
		}

		if seen[m.ID] {
			return fmt.Sprintf("materials[%d].id appears more than once", i)
		}
		seen[m.ID] = true

		switch {
		case m.QuantityBase < 0:
			return fmt.Sprintf("materials[%d].quantity_base must not be negative", i)

		case m.QuantityPerParticipant < 0:
			return fmt.Sprintf(
				"materials[%d].quantity_per_participant must not be negative", i)

		case m.QuantityBase == 0 && m.QuantityPerParticipant == 0:
			return fmt.Sprintf(
				"materials[%d] must resolve to at least one item", i)
		}
	}

	return ""
}

func (req editGameMaterialsRequest) materials() []store.GameMaterialInput {
	materials := make([]store.GameMaterialInput, 0, len(*req.Materials))
	for _, m := range *req.Materials {
		materials = append(materials, store.GameMaterialInput{
			ID:                     m.ID,
			QuantityBase:           m.QuantityBase,
			QuantityPerParticipant: m.QuantityPerParticipant,
			Optional:               m.Optional,
		})
	}

	return materials
}

func (s *Server) handleEditGameMaterials(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		s.writeUnauthorized(w, msgNotAuthenticated)
		return
	}

	gameID := chi.URLParam(r, "id")

	var parsed pgtype.UUID
	if err := parsed.Scan(gameID); err != nil {
		s.writeBadRequest(w, "malformed game id")
		return
	}

	err := store.AuthorizeGameWrite(r.Context(), s.pool, gameID, userID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	var req editGameMaterialsRequest
	if err := readJSON(w, r, &req); err != nil {
		s.writeBadRequest(w, err.Error())
		return
	}

	if msg := req.validate(); msg != "" {
		s.writeUnprocessable(w, msg)
		return
	}

	game, err := store.EditGameMaterials(
		r.Context(), s.pool, gameID, req.materials(), userID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	if err := writeJSON(w, http.StatusOK, newGameDetail(game)); err != nil {
		slog.Error("Failed to write edit game materials response", "error", err)
	}
}

func publishPreconditions(game store.GameDetail) []string {
	var failures []string

	if len(game.Blocks) == 0 {
		failures = append(failures, "a published game needs at least one block")
	}
	if len(game.Categories) == 0 {
		failures = append(failures, "a published game needs at least one category")
	}
	if strings.TrimSpace(game.Description) == "" {
		failures = append(failures, "a published game needs a description")
	}

	return failures
}

func (s *Server) handlePublishGame(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	gameID := chi.URLParam(r, "id")
	var parsed pgtype.UUID
	if err := parsed.Scan(gameID); err != nil {
		s.writeBadRequest(w, "malformed game id")
		return
	}
	userID, _ := userIDFromContext(ctx)

	err := store.AuthorizeGameWrite(ctx, s.pool, gameID, userID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	draft, err := store.GetGameByID(ctx, s.pool, gameID, userID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	if failures := publishPreconditions(draft); len(failures) > 0 {
		s.writeIncomplete(w, failures)
		return
	}

	game, err := store.PublishGame(ctx, s.pool, gameID, userID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	err = writeJSON(w, http.StatusOK, newGameDetail(game))
	if err != nil {
		slog.Error("Failed to write publish game response", "error", err)
	}

}

func (s *Server) handleUnpublishGame(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	gameID := chi.URLParam(r, "id")
	userID, _ := userIDFromContext(ctx)

	var parsed pgtype.UUID
	if err := parsed.Scan(gameID); err != nil {
		s.writeBadRequest(w, "malformed game id")
		return
	}

	err := store.AuthorizeGameWrite(ctx, s.pool, gameID, userID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	game, err := store.UnpublishGame(ctx, s.pool, gameID, userID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	err = writeJSON(w, http.StatusOK, newGameDetail(game))
	if err != nil {
		slog.Error("Failed to write unpublish game response", "error", err)
	}

}
