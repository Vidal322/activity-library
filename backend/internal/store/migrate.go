package store

import (
	"context"
	"embed"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Migrations are embedded so the binary carries its own schema: the image
// needs no SQL files alongside it, and there is no separate "run migrations"
// step to forget on deploy.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies every pending migration and returns once the schema is
// current. Callers run it after InitPool and before serving traffic.
//
// This assumes one instance migrating at a time. Replicas booting together
// would race here, and that is when this should move to goose's Provider API,
// which takes a session lock.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(gooseLogger{})

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}

	// goose speaks database/sql, so borrow the pool through pgx's stdlib
	// adapter rather than opening a second connection off the DSN. Closing
	// this handle releases the adapter only; the pool stays open.
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()

	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	return nil
}

// gooseLogger routes goose's own output through slog so migration progress
// lands in the same structured stream as everything else.
type gooseLogger struct{}

func (gooseLogger) Printf(format string, v ...any) {
	slog.Info(fmt.Sprintf(format, v...))
}

// Fatalf must not exit: goose calls it on failure, but Up already returns the
// error, and main owns the exit path so deferred cleanup still runs.
func (gooseLogger) Fatalf(format string, v ...any) {
	slog.Error(fmt.Sprintf(format, v...))
}
