package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Vidal322/activity-library/internal/auth"
	"github.com/Vidal322/activity-library/internal/store"
)

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (req loginRequest) validate() string {
	switch {
	case req.Email == "", req.Password == "":
		return "email and password are required"

	case len(req.Email) > maxEmail:
		return fmt.Sprintf("email must not be longer than %d characters", maxEmail)

	case len(req.Password) > maxPassword:
		return fmt.Sprintf("password must not be longer than %d characters", maxPassword)
	}

	return ""
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	err := readJSON(w, r, &req)
	if err != nil {
		s.writeBadRequest(w, err.Error())
		return
	}

	req.Email = strings.TrimSpace(req.Email)

	msg := req.validate()
	if msg != "" {
		s.writeUnprocessable(w, msg)
		return
	}

	user, err := store.GetUserByEmail(r.Context(), s.pool, req.Email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			auth.VerifyDummy(req.Password)
			s.writeUnauthorized(w, msgInvalidCredentials)
			return
		}
		s.writeStoreError(w, err)
		return
	}

	decision, err := auth.Verify(req.Password, user.PassHash)
	if err != nil {
		s.writeInternalError(
			w,
			"Stored password hash is unreadable",
			"user_id",
			user.ID,
			"error",
			err,
		)
		return
	}

	if !decision || !user.Active {
		s.writeUnauthorized(w, msgInvalidCredentials)
		return
	}

	token, err := auth.NewSessionToken()
	if err != nil {
		s.writeInternalError(w, "Could not generate a session token", "error", err)
		return
	}

	expiresAt := time.Now().Add(s.config.Session.TTL)
	_, err = store.CreateSession(
		r.Context(),
		s.pool,
		user.ID,
		auth.HashToken(token),
		expiresAt,
	)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	s.setSessionCookie(w, token)
	newUserDetails := newUserDetail(user)
	err = writeJSON(w, http.StatusOK, newUserDetails)
	if err != nil {
		slog.Error("Failed to write session created response", "error", err)
		return
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		s.writeUnauthorized(w, msgNotAuthenticated)
		return
	}
	token := c.Value
	tokenHash := auth.HashToken(token)
	err = store.DeleteSession(r.Context(), s.pool, tokenHash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.clearSessionCookie(w)
			s.writeUnauthorized(w, msgNotAuthenticated)
			return
		}
		s.writeStoreError(w, err)
		return
	}

	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}
