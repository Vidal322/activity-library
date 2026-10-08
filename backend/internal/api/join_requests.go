package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Vidal322/activity-library/internal/store"
)

const maxJoinRequestMessage = 1000

type createJoinRequestRequest struct {
	Message string `json:"message"`
}

func (req createJoinRequestRequest) validate() string {
	if utf8.RuneCountInString(req.Message) > maxJoinRequestMessage {
		return fmt.Sprintf("message must not be longer than %d characters", maxJoinRequestMessage)
	}

	return ""
}

type joinRequestDetail struct {
	ID          string     `json:"id"`
	RequesterID string     `json:"requester_id"`
	Message     string     `json:"message"`
	State       string     `json:"state"`
	DecidedBy   *string    `json:"decided_by"`
	CreatedAt   time.Time  `json:"created_at"`
	DecidedAt   *time.Time `json:"decided_at"`
}

func newJoinRequestDetail(joinReq store.JoinRequest) joinRequestDetail {
	return joinRequestDetail{
		ID:          joinReq.ID,
		RequesterID: joinReq.RequesterID,
		Message:     joinReq.Message,
		State:       joinReq.State,
		DecidedBy:   joinReq.DecidedBy,
		CreatedAt:   joinReq.CreatedAt,
		DecidedAt:   joinReq.DecidedAt,
	}
}

// TODO: middleware for only non members
func (s *Server) handleCreateJoinRequest(w http.ResponseWriter, r *http.Request) {
	requesterID, ok := userIDFromContext(r.Context())
	if !ok {
		s.writeInternalError(w, "no user on an authenticated route", "path", r.URL.Path)
		return
	}

	var req createJoinRequestRequest
	err := readJSON(w, r, &req)
	if err != nil {
		s.writeBadRequest(w, err.Error())
		return
	}

	req.Message = strings.TrimSpace(req.Message)

	msg := req.validate()
	if msg != "" {
		s.writeUnprocessable(w, msg)
		return
	}

	joinReq, err := store.CreateJoinRequest(r.Context(), s.pool, requesterID, req.Message)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	err = writeJSON(w, http.StatusCreated, newJoinRequestDetail(joinReq))
	if err != nil {
		slog.Error("Could not write join request created response", "error", err)
	}
}
