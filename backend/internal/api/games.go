package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

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

	if q != nil {
		offset, err := parseOffsetCursor(query["cursor"])
		if err != nil {
			s.writeBadRequest(w, "cursor: "+err.Error())
			return
		}

		found, err := store.SearchGames(r.Context(), s.pool, *q, filter, limit, offset)
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
			store.GamePage{Limit: limit, Cursor: cursor})
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

	game, err := store.GetGameByID(r.Context(), s.pool, gameID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	if err := writeJSON(w, http.StatusOK, newGameDetail(game)); err != nil {
		slog.Error("Failed to write get game response", "error", err)
	}
}
