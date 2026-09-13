package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Vidal322/activity-library/internal/store"
)

type userDetail struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Img       *string   `json:"img"`
	Role      string    `json:"role"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func newUserDetail(u store.User) userDetail {
	return userDetail{
		ID:        u.ID,
		Name:      u.Name,
		Img:       u.Img,
		Role:      u.Role,
		Active:    u.Active,
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")

	var parsed pgtype.UUID
	if err := parsed.Scan(userID); err != nil {
		s.writeBadRequest(w, "malformed user id")
		return
	}

	user, err := store.GetUserByID(r.Context(), s.pool, userID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	err = writeJSON(w, http.StatusOK, newUserDetail(user))
	if err != nil {
		slog.Error("Failed to write get user response", "error", err)
	}
}
