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

	if err := writeJSON(w, http.StatusOK, newGameSummary(game)); err != nil {
		slog.Error("Failed to write get game response", "error", err)
	}
}
