package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/Vidal322/activity-library/internal/auth"
	"github.com/Vidal322/activity-library/internal/store"
)

// invitationDetail is what an admin sees for an invitation. The token hash is
// left out entirely: it is a lookup key, and nothing outside the store needs it.
type invitationDetail struct {
	ID         string     `json:"id"`
	Email      string     `json:"email"`
	InvitedBy  string     `json:"invited_by"`
	AcceptedBy *string    `json:"accepted_by"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	AcceptedAt *time.Time `json:"accepted_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

func newInvitationDetail(i store.Invitation) invitationDetail {
	return invitationDetail{
		ID:         i.ID,
		Email:      i.Email,
		InvitedBy:  i.InvitedBy,
		AcceptedBy: i.AcceptedBy,
		CreatedAt:  i.CreatedAt,
		ExpiresAt:  i.ExpiresAt,
		AcceptedAt: i.AcceptedAt,
		RevokedAt:  i.RevokedAt,
	}
}

// token is passed temporarly while the email service is not setup
type createdInvitation struct {
	invitationDetail
	Token string `json:"token"`
}

type createInvitationRequest struct {
	Email string `json:"email"`
}

func (req createInvitationRequest) validate() string {
	switch {
	case req.Email == "":
		return "email is required"

	case len(req.Email) > maxEmail:
		return fmt.Sprintf("email must not be longer than %d characters", maxEmail)
	}

	addr, err := mail.ParseAddress(req.Email)
	if err != nil || addr.Address != req.Email {
		return "email is not a valid address"
	}

	return ""
}

func (s *Server) handleCreateInvitation(w http.ResponseWriter, r *http.Request) {
	invitedBy, ok := userIDFromContext(r.Context())
	if !ok {
		s.writeInternalError(w, "no user on an authenticated route", "path", r.URL.Path)
		return
	}

	var req createInvitationRequest
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

	token, err := auth.NewToken()
	if err != nil {
		s.writeInternalError(w, "Could not generate an invitation token", "error", err)
		return
	}

	invitation, err := store.CreateInvitation(
		r.Context(),
		s.pool,
		req.Email,
		invitedBy,
		auth.HashToken(token),
		time.Now().Add(s.config.Invitation.TTL),
	)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	err = writeJSON(w, http.StatusCreated, createdInvitation{
		invitationDetail: newInvitationDetail(invitation),
		Token:            token,
	})
	if err != nil {
		slog.Error("Could not write invitation created response", "error", err)
		return
	}
}
