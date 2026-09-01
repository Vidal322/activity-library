package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

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

// parseIDParam validates each repeated value of one query parameter as a UUID
// and drops duplicates, which the store's AND match cannot tolerate.
func parseIDParam(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}

	ids := make([]string, 0, len(values))
	seen := make(map[[16]byte]struct{}, len(values))

	for _, v := range values {
		var parsed pgtype.UUID
		if err := parsed.Scan(v); err != nil {
			return nil, fmt.Errorf("%q is not a valid id", v)
		}
		if _, dup := seen[parsed.Bytes]; dup {
			continue
		}
		seen[parsed.Bytes] = struct{}{}
		ids = append(ids, v)
	}

	return ids, nil
}

// singleValue pulls the one value a scalar filter admits. Repeats are rejected
// rather than resolved: the rail sets a single value per parameter, so two of
// them means the URL is malformed, and quietly honouring one of the pair hides
// that. The bool reports whether the parameter was there at all.
func singleValue(values []string) (string, bool, error) {
	switch len(values) {
	case 0:
		return "", false, nil
	case 1:
		return values[0], true, nil
	default:
		return "", false, errors.New("expects a single value")
	}
}

// parseIntParam reads one scalar filter, nil being the absent one. Zero and
// negatives are refused along with the unparseable: the columns' CHECKs keep
// every stored bound above zero, so such a value could only ever match nothing,
// and an empty list reads as missing data rather than as a bad request.
func parseIntParam(values []string) (*int32, error) {
	raw, present, err := singleValue(values)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}

	// bitSize 32 makes anything the integer column could not hold an error
	// here rather than a wrapped value further down.
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || n <= 0 {
		return nil, fmt.Errorf("%q is not a positive whole number", raw)
	}

	parsed := int32(n)
	return &parsed, nil
}

// parseBoolParam reads one boolean filter. Absent is nil and false is a filter
// in its own right, so the three states stay apart.
func parseBoolParam(values []string) (*bool, error) {
	raw, present, err := singleValue(values)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}

	switch raw {
	case "true":
		parsed := true
		return &parsed, nil
	case "false":
		parsed := false
		return &parsed, nil
	default:
		return nil, fmt.Errorf("%q is not true or false", raw)
	}
}

// handleGamesList serves GET /v1/games. The category and location parameters
// repeat and AND together, so ?category=a&category=b asks for the games
// carrying both. participants, duration and no_materials take a single value
// each, and every filter given has to hold at once.
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

	games, err := store.ListGames(r.Context(), s.pool, store.GameFilter{
		CategoryIDs:  categoryIDs,
		LocationIDs:  locationIDs,
		Participants: participants,
		Duration:     duration,
		NoMaterials:  noMaterials,
	})
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
