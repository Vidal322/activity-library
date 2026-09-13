package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Vidal322/activity-library/internal/auth"
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

type createUserRequest struct {
	Name     string  `json:"name"`
	Email    string  `json:"email"`
	Password string  `json:"password"`
	Img      *string `json:"img"`
}

const (
	maxName     = 100
	maxEmail    = 254
	minPassword = 8
	maxPassword = 1024
)

func (req createUserRequest) validate() string {
	switch {
	case req.Name == "", req.Email == "", req.Password == "":
		return "name, email and password are required"

	case utf8.RuneCountInString(req.Name) > maxName:
		return fmt.Sprintf("name must not be longer than %d characters", maxName)

	case len(req.Email) > maxEmail:
		return fmt.Sprintf("email must not be longer than %d characters", maxEmail)

	case utf8.RuneCountInString(req.Password) < minPassword:
		return fmt.Sprintf("password must be at least %d characters", minPassword)

	case len(req.Password) > maxPassword:
		return fmt.Sprintf("password must not be longer than %d characters", maxPassword)
	}

	addr, err := mail.ParseAddress(req.Email)
	if err != nil || addr.Address != req.Email {
		return "email is not a valid address"
	}

	return ""
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	err := readJSON(w, r, &req)
	if err != nil {
		s.writeBadRequest(w, err.Error())
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Email = strings.TrimSpace(req.Email)

	if msg := req.validate(); msg != "" {
		s.writeBadRequest(w, msg)
		return
	}

	hash, err := auth.Hash(req.Password)
	if err != nil {
		slog.Error("Could not hash a password", "error", err)
		err = writeErrorJSON(w, http.StatusInternalServerError, "internal server error")
		if err != nil {
			slog.Error("Failed to write create user response", "error", err)
		}
		return
	}

	user, err := store.CreateUser(r.Context(), s.pool, req.Name, req.Email, hash, req.Img)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	w.Header().Set("Location", apiPrefix+"/users/"+user.ID)

	err = writeJSON(w, http.StatusCreated, newUserDetail(user))
	if err != nil {
		slog.Error("Failed to write create user response", "error", err)
		return
	}
}
