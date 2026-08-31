package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Vidal322/activity-library/internal/store"
)

// These tests need no database. The store hands up sentinels, so every case
// here is a sentinel or a ConstraintError built by hand.

func TestStoreErrorResponse(t *testing.T) {
	tests := []struct {
		name       string
		in         error
		wantStatus int
		wantMsg    string
	}{
		{
			name:       "not found",
			in:         store.ErrNotFound,
			wantStatus: http.StatusNotFound,
			wantMsg:    "not found",
		},
		{
			name:       "named conflict names the field",
			in:         &store.ConstraintError{Constraint: "users_email_key", Sentinel: store.ErrConflict},
			wantStatus: http.StatusConflict,
			wantMsg:    "that email is already registered",
		},
		{
			name:       "bare conflict uses the generic wording",
			in:         store.ErrConflict,
			wantStatus: http.StatusConflict,
			wantMsg:    "already exists",
		},
		{
			// A constraint added by a later migration must degrade to the
			// generic wording rather than reach the client empty.
			name:       "unmapped conflict falls back",
			in:         &store.ConstraintError{Constraint: "some_future_key", Sentinel: store.ErrConflict},
			wantStatus: http.StatusConflict,
			wantMsg:    "already exists",
		},
		{
			// This pair is the whole design: one constraint name, two meanings.
			// Postgres reports the same name whether the insert referenced a
			// missing category or the delete hit a category still in use.
			name:       "foreign key as bad input",
			in:         &store.ConstraintError{Constraint: "game_categories_category_id_fkey", Sentinel: store.ErrInvalid},
			wantStatus: http.StatusUnprocessableEntity,
			wantMsg:    "unknown category",
		},
		{
			name:       "foreign key as still in use",
			in:         &store.ConstraintError{Constraint: "game_categories_category_id_fkey", Sentinel: store.ErrInUse},
			wantStatus: http.StatusConflict,
			wantMsg:    "that category is still used by a game",
		},
		{
			name:       "check violation names the rule",
			in:         &store.ConstraintError{Constraint: "games_duration_range", Sentinel: store.ErrInvalid},
			wantStatus: http.StatusUnprocessableEntity,
			wantMsg:    "minimum duration must not exceed maximum duration",
		},
		{
			name:       "bare invalid uses the generic wording",
			in:         store.ErrInvalid,
			wantStatus: http.StatusUnprocessableEntity,
			wantMsg:    "invalid request",
		},
		{
			name:       "bare in use uses the generic wording",
			in:         store.ErrInUse,
			wantStatus: http.StatusConflict,
			wantMsg:    "still referenced by other records",
		},
		{
			// The only branch that answers with the error's own text: it is
			// built from the ids the client sent, not from anything internal.
			name: "unknown filter id names the ids",
			in: &store.UnknownFilterError{
				Field: "category",
				IDs:   []string{"30000000-0000-7000-8000-0000000000ff"},
			},
			wantStatus: http.StatusBadRequest,
			wantMsg:    "unknown category: 30000000-0000-7000-8000-0000000000ff",
		},
		{
			// Wrapped, since a store call may add context on the way out.
			name: "wrapped unknown filter id still maps",
			in: fmt.Errorf("listing games: %w", &store.UnknownFilterError{
				Field: "location",
				IDs:   []string{"40000000-0000-7000-8000-0000000000ff"},
			}),
			wantStatus: http.StatusBadRequest,
			wantMsg:    "unknown location: 40000000-0000-7000-8000-0000000000ff",
		},
		{
			// Anything the store did not classify is ours, not the caller's,
			// and must not describe itself to the client.
			name:       "unclassified error is internal",
			in:         errors.New("dial tcp: connection refused"),
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "internal server error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, msg := storeErrorResponse(tt.in)

			if status != tt.wantStatus {
				t.Errorf("status = %d, want %d", status, tt.wantStatus)
			}
			if msg != tt.wantMsg {
				t.Errorf("message = %q, want %q", msg, tt.wantMsg)
			}
		})
	}
}

// TestWriteStoreErrorWritesJSON covers the half storeErrorResponse never
// touches: that the status and message actually reach the response.
func TestWriteStoreErrorWritesJSON(t *testing.T) {
	rec := httptest.NewRecorder()

	s := &Server{}
	s.writeStoreError(rec, &store.ConstraintError{
		Constraint: "users_email_key",
		Sentinel:   store.ErrConflict,
	})

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want %d", res.StatusCode, http.StatusConflict)
	}
	if got := res.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}

	var body map[string]string
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	const want = "that email is already registered"
	if body["error"] != want {
		t.Errorf("body[error] = %q, want %q", body["error"], want)
	}
}

// TestWriteStoreErrorHidesInternalDetail guards the one leak that matters: an
// unclassified error carries driver and connection detail, and the client must
// see none of it.
func TestWriteStoreErrorHidesInternalDetail(t *testing.T) {
	rec := httptest.NewRecorder()

	s := &Server{}
	s.writeStoreError(rec, errors.New("dial tcp 10.0.0.5:5432: connection refused"))

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", res.StatusCode, http.StatusInternalServerError)
	}

	var body map[string]string
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if body["error"] != "internal server error" {
		t.Errorf("body[error] = %q, want the generic message", body["error"])
	}
}
