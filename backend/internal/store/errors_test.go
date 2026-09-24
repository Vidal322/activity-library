package store

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// These tests need no database. classify reads only Code and ConstraintName
// off the driver error, so every case here is a PgError built by hand.

// sentinels lets a case assert the result matches exactly one of them. Checking
// only the expected sentinel would pass an error that matched two.
var sentinels = []error{ErrNotFound, ErrConflict, ErrInvalid, ErrInUse, ErrReferenced}

// assertClassified checks the three things a caller depends on: the error is
// unchanged when nothing recognised it, it matches the one expected sentinel,
// and it carries the constraint name (or deliberately carries none).
func assertClassified(t *testing.T, in, got, wantSentinel error, wantConstraint string) {
	t.Helper()

	if wantSentinel == nil {
		// Identity, not just "some error": the 500 path logs this value, and a
		// classifier that rewrapped it would lose the original message.
		if got != in {
			t.Fatalf("got %#v, want the original error unchanged", got)
		}
		return
	}

	for _, sentinel := range sentinels {
		want := sentinel == wantSentinel
		if errors.Is(got, sentinel) != want {
			t.Errorf("errors.Is(got, %v) = %t, want %t", sentinel, !want, want)
		}
	}

	var ce *ConstraintError

	if wantConstraint == "" {
		if errors.As(got, &ce) {
			t.Errorf("got a ConstraintError naming %q, want the bare sentinel", ce.Constraint)
		}
		return
	}

	if !errors.As(got, &ce) {
		t.Fatalf("got %#v, want a ConstraintError naming %q", got, wantConstraint)
	}
	if ce.Constraint != wantConstraint {
		t.Errorf("constraint = %q, want %q", ce.Constraint, wantConstraint)
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		in   error
		// wantSentinel nil means the error is expected to pass through unchanged.
		wantSentinel error
		// wantConstraint "" means no ConstraintError is expected.
		wantConstraint string
	}{
		{
			name: "nil stays nil",
		},
		{
			name:         "no rows is not found",
			in:           pgx.ErrNoRows,
			wantSentinel: ErrNotFound,
		},
		{
			name:         "wrapped no rows is still not found",
			in:           fmt.Errorf("find user: %w", pgx.ErrNoRows),
			wantSentinel: ErrNotFound,
		},
		{
			name:           "unique violation is a conflict",
			in:             &pgconn.PgError{Code: "23505", ConstraintName: "users_email_key"},
			wantSentinel:   ErrConflict,
			wantConstraint: "users_email_key",
		},
		{
			name:           "foreign key violation is invalid input",
			in:             &pgconn.PgError{Code: "23503", ConstraintName: "game_categories_category_id_fkey"},
			wantSentinel:   ErrInvalid,
			wantConstraint: "game_categories_category_id_fkey",
		},
		{
			name:           "check violation is invalid input",
			in:             &pgconn.PgError{Code: "23514", ConstraintName: "games_duration_range"},
			wantSentinel:   ErrInvalid,
			wantConstraint: "games_duration_range",
		},
		{
			name:           "exclusion violation is invalid input",
			in:             &pgconn.PgError{Code: "23P01", ConstraintName: "some_exclusion"},
			wantSentinel:   ErrInvalid,
			wantConstraint: "some_exclusion",
		},
		{
			// Postgres names no constraint for 23502, only the column, so the
			// result must be the bare sentinel rather than an empty name.
			name:         "not null violation names no constraint",
			in:           &pgconn.PgError{Code: "23502", ColumnName: "email"},
			wantSentinel: ErrInvalid,
		},
		{
			name: "unrecognised sqlstate passes through",
			in:   &pgconn.PgError{Code: "42P01", Message: "relation does not exist"},
		},
		{
			name: "non-database error passes through",
			in:   errors.New("dial tcp: connection refused"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classify(tt.in)
			assertClassified(t, tt.in, got, tt.wantSentinel, tt.wantConstraint)
		})
	}
}

func TestClassifyDelete(t *testing.T) {
	tests := []struct {
		name           string
		in             error
		wantSentinel   error
		wantConstraint string
	}{
		{
			// The same constraint reads as invalid input on an insert. Only the
			// caller knows which direction the violation came from.
			name:           "foreign key violation means still in use",
			in:             &pgconn.PgError{Code: "23503", ConstraintName: "categories_family_id_fkey"},
			wantSentinel:   ErrInUse,
			wantConstraint: "categories_family_id_fkey",
		},
		{
			name:           "unique violation still conflicts",
			in:             &pgconn.PgError{Code: "23505", ConstraintName: "locations_name_key"},
			wantSentinel:   ErrConflict,
			wantConstraint: "locations_name_key",
		},
		{
			name:         "no rows is still not found",
			in:           pgx.ErrNoRows,
			wantSentinel: ErrNotFound,
		},
		{
			name: "nil stays nil",
		},
		{
			name: "non-database error passes through",
			in:   errors.New("dial tcp: connection refused"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyDelete(tt.in)
			assertClassified(t, tt.in, got, tt.wantSentinel, tt.wantConstraint)
		})
	}
}

func TestClassifyUpdate(t *testing.T) {
	tests := []struct {
		name           string
		in             error
		wantSentinel   error
		wantConstraint string
	}{
		{
			// Setting games.no_materials under existing game_materials rows.
			// The insert that runs into the same constraint reads as invalid.
			name:           "foreign key violation means still referenced",
			in:             &pgconn.PgError{Code: "23503", ConstraintName: "game_materials_game_fkey"},
			wantSentinel:   ErrReferenced,
			wantConstraint: "game_materials_game_fkey",
		},
		{
			name:           "check violation is still invalid",
			in:             &pgconn.PgError{Code: "23514", ConstraintName: "games_duration_range"},
			wantSentinel:   ErrInvalid,
			wantConstraint: "games_duration_range",
		},
		{
			name:         "no rows is still not found",
			in:           pgx.ErrNoRows,
			wantSentinel: ErrNotFound,
		},
		{
			name: "nil stays nil",
		},
		{
			name: "non-database error passes through",
			in:   errors.New("dial tcp: connection refused"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyUpdate(tt.in)
			assertClassified(t, tt.in, got, tt.wantSentinel, tt.wantConstraint)
		})
	}
}
