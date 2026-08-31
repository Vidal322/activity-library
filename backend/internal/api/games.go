package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Vidal322/activity-library/internal/store"
)

// gameSummary is the card the library grid renders
type gameSummary struct {
	ID              string  `json:"id"`
	Title           string  `json:"title"`
	Description     string  `json:"description"`
	Image           *string `json:"image"`
	MinParticipants *int32  `json:"min_participants"`
	MaxParticipants *int32  `json:"max_participants"`
	DurationMin     *int32  `json:"duration_min"`
	DurationMax     *int32  `json:"duration_max"`
	NoMaterials     bool    `json:"no_materials"`
}

type gamesListResponse struct {
	Games []gameSummary `json:"games"`
}

func newGameSummary(g store.Game) gameSummary {
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
	}
}

// gameCategory carries its family alongside the category, so the detail page
// can group the badges without resolving families itself.
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

// gameDetail embeds the summary so a card and a detail page read the same
// spine fields, and adds the associations. The embed is untagged on purpose:
// tagging it would nest the spine under a key instead of flattening it, and the
// detail payload would stop being a superset of the card. All three arrays are
// always present, so an empty one serializes as [] rather than null.
type gameDetail struct {
	gameSummary
	Categories []gameCategory `json:"categories"`
	Locations  []gameLocation `json:"locations"`
	Materials  []gameMaterial `json:"materials"`
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

	return gameDetail{
		gameSummary: newGameSummary(g.Game),
		Categories:  categories,
		Locations:   locations,
		Materials:   materials,
	}
}

// handleGamesList serves GET /v1/games.
func (s *Server) handleGamesList(w http.ResponseWriter, r *http.Request) {
	games, err := store.ListGames(r.Context(), s.pool)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	summaries := make([]gameSummary, 0, len(games))
	for _, g := range games {
		summaries = append(summaries, newGameSummary(g))
	}

	if err := writeJSON(w, http.StatusOK, gamesListResponse{Games: summaries}); err != nil {
		slog.Error("Failed to write games list response", "error", err)
	}
}

// handleGetGame serves GET /v1/games/{id}.
func (s *Server) handleGetGame(w http.ResponseWriter, r *http.Request) {
	gameID := chi.URLParam(r, "id")

	var parsed pgtype.UUID
	if err := parsed.Scan(gameID); err != nil {
		if err := writeErrorJSON(w, http.StatusBadRequest, "malformed game id"); err != nil {
			slog.Error("Failed to write get game error response", "error", err)
		}
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
