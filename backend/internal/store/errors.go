package store

import (
	"errors"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	ErrInvalid  = errors.New("invalid")
	ErrInUse    = errors.New("in use")
)

type ConstraintError struct {
	Constraint string
	Sentinel   error
}

func (e *ConstraintError) Error() string {
	return e.Constraint + ": " + e.Sentinel.Error()
}

func (e *ConstraintError) Unwrap() error {
	return e.Sentinel
}

func constraintErr(name string, sentinel error) error {
	if name == "" {
		return sentinel
	}
	return &ConstraintError{Constraint: name, Sentinel: sentinel}
}

func classify(err error) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}

	var pgErr *pgconn.PgError

	if !errors.As(err, &pgErr) {
		return err
	}

	switch pgErr.Code {
	case pgerrcode.UniqueViolation:
		return constraintErr(pgErr.ConstraintName, ErrConflict)
	case pgerrcode.ForeignKeyViolation, pgerrcode.CheckViolation,
		pgerrcode.NotNullViolation, pgerrcode.ExclusionViolation:
		return constraintErr(pgErr.ConstraintName, ErrInvalid)
	default:
		return err
	}
}

func classifyDelete(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.ForeignKeyViolation {
		return constraintErr(pgErr.ConstraintName, ErrInUse)
	}
	return classify(err)
}
