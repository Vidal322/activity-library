package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/Vidal322/activity-library/internal/store"
)

const (
	msgInvalidCredentials = "invalid email or password"
	msgNotAuthenticated   = "authentication required"
	msgForbidden          = "you did not write this game"
	msgInternalError      = "internal server error"
)

var conflictMessages = map[string]string{
	"users_email_key":            "that email is already registered",
	"category_families_name_key": "a family with that name already exists",
	"categories_family_name_key": "a category with that name already exists in this family",
	"locations_name_key":         "a location with that name already exists",
	"materials_name_key":         "a material with that name already exists",
	"blocks_game_position_key":   "another block already occupies that position",
	"game_authors_pkey":          "that user is already an author of this game",
	"game_categories_pkey":       "that category is already on this game",
	"game_locations_pkey":        "that location is already on this game",
	"game_materials_pkey":        "that material is already on this game",
	"game_inspirations_pkey":     "that game is already listed as an inspiration",
}

var invalidMessages = map[string]string{
	"users_name_check":                            "name must not be empty",
	"users_email_check":                           "email must not be empty",
	"category_families_name_check":                "name must not be empty",
	"categories_name_check":                       "name must not be empty",
	"locations_name_check":                        "name must not be empty",
	"materials_name_check":                        "name must not be empty",
	"games_title_check":                           "title must not be empty",
	"games_min_participants_check":                "minimum participants must be greater than zero",
	"games_max_participants_check":                "maximum participants must be greater than zero",
	"games_duration_min_check":                    "minimum duration must be greater than zero",
	"games_duration_max_check":                    "maximum duration must be greater than zero",
	"games_participants_range":                    "minimum participants must not exceed maximum participants",
	"games_duration_range":                        "minimum duration must not exceed maximum duration",
	"games_non_variant_complete":                  "participants and duration are required unless the game is a variant",
	"games_publish_state_check":                   "publish state must be draft or published",
	"blocks_type_check":                           "unknown block type",
	"blocks_position_check":                       "position must not be negative",
	"game_materials_quantity_positive":            "a material requirement must resolve to at least one item",
	"game_materials_per_participant_non_negative": "quantity per participant must not be negative",
	"game_materials_game_no_materials_check":      "this game is marked as needing no materials",
	"game_inspirations_not_self":                  "a game cannot inspire itself",
	"games_author_required":                       "a game must have at least one author",

	"game_authors_user_id_fkey":             "unknown author",
	"games_original_not_variant_fkey":       "the original game does not exist, or is itself a variant",
	"categories_family_id_fkey":             "unknown category family",
	"blocks_game_id_fkey":                   "unknown game",
	"game_categories_game_id_fkey":          "unknown game",
	"game_categories_category_id_fkey":      "unknown category",
	"game_locations_game_id_fkey":           "unknown game",
	"game_locations_location_id_fkey":       "unknown location",
	"game_inspirations_game_id_fkey":        "unknown game",
	"game_inspirations_inspiration_id_fkey": "unknown inspiration game",
	"game_materials_material_id_fkey":       "unknown material",
	"game_materials_game_fkey":              "this game is marked as needing no materials; unset no_materials before adding any",
}

var referencedMessages = map[string]string{
	"game_materials_game_fkey": "this game still lists materials; remove them before marking it as needing no materials",
}

var inUseMessages = map[string]string{
	"categories_family_id_fkey":        "that family still has categories",
	"game_authors_user_id_fkey":        "that user still has games",
	"games_original_not_variant_fkey":  "that game still has variants",
	"game_categories_category_id_fkey": "that category is still used by a game",
	"game_locations_location_id_fkey":  "that location is still used by a game",
	"game_materials_material_id_fkey":  "that material is still used by a game",
}

func (s *Server) writeStoreError(w http.ResponseWriter, err error) {
	status, msg := storeErrorResponse(err)

	if status == http.StatusInternalServerError {
		slog.Error("Unhandled store error", "error", err)
	}

	if writeErr := writeErrorJSON(w, status, msg); writeErr != nil {
		slog.Error("Failed to write error response", "error", writeErr)
	}
}

func storeErrorResponse(err error) (int, string) {
	var unknownFilter *store.UnknownFilterError
	var unknownBlock *store.UnknownBlockError
	var unknownAssociation *store.UnknownAssociationError

	switch {
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound, "not found"

	case errors.As(err, &unknownFilter):
		return http.StatusBadRequest, unknownFilter.Error()

	case errors.As(err, &unknownBlock):
		return http.StatusUnprocessableEntity, unknownBlock.Error()

	case errors.As(err, &unknownAssociation):
		return http.StatusUnprocessableEntity, unknownAssociation.Error()

	case errors.Is(err, store.ErrInUse):
		return http.StatusConflict, message(err, inUseMessages, "still referenced by other records")

	case errors.Is(err, store.ErrReferenced):
		return http.StatusUnprocessableEntity, message(
			err,
			referencedMessages,
			"still referenced by other records",
		)

	case errors.Is(err, store.ErrConflict):
		return http.StatusConflict, message(err, conflictMessages, "already exists")

	case errors.Is(err, store.ErrForbidden):
		return http.StatusForbidden, msgForbidden

	case errors.Is(err, store.ErrInvalid):
		return http.StatusUnprocessableEntity, message(err, invalidMessages, "invalid request")

	default:
		return http.StatusInternalServerError, msgInternalError
	}
}

func message(err error, messages map[string]string, fallback string) string {
	var ce *store.ConstraintError
	if !errors.As(err, &ce) {
		return fallback
	}
	if msg, ok := messages[ce.Constraint]; ok {
		return msg
	}
	return fallback
}

func (s *Server) writeBadRequest(w http.ResponseWriter, msg string) {
	if err := writeErrorJSON(w, http.StatusBadRequest, msg); err != nil {
		slog.Error("Failed to write bad request response", "error", err)
	}
}

func (s *Server) writeUnprocessable(w http.ResponseWriter, msg string) {
	if err := writeErrorJSON(w, http.StatusUnprocessableEntity, msg); err != nil {
		slog.Error("Failed to write unprocessable entity response", "error", err)
	}
}

func (s *Server) writeUnauthorized(w http.ResponseWriter, msg string) {
	if err := writeErrorJSON(w, http.StatusUnauthorized, msg); err != nil {
		slog.Error("Failed to write unauthorized request response", "error", err)
	}
}

func (s *Server) writeInternalError(w http.ResponseWriter, msg string, args ...any) {
	slog.Error(msg, args...)

	if err := writeErrorJSON(w, http.StatusInternalServerError, msgInternalError); err != nil {
		slog.Error("Failed to write internal error response", "error", err)
	}
}
