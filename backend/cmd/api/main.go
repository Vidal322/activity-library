package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/joho/godotenv"

	"github.com/Vidal322/activity-library/internal/store"
)

const dbStartupTimeout = 10 * time.Second

var version = "dev"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	slog.SetDefault(logger)

	if err := run(); err != nil {
		slog.Error("Fatal error", "error", err)
		os.Exit(1)
	}
}

// run holds what main used to do. os.Exit skips deferred calls, so keeping it
// in main alone is what lets the deferred pool.Close below actually run.
func run() error {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read .env: %w", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), dbStartupTimeout)
	defer cancel()

	pool, err := store.InitPool(ctx, store.PoolConfig{
		DSN:      cfg.DB.DSN,
		MaxConns: cfg.DB.MaxConns,
		MinConns: cfg.DB.MinConns,
	})
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	slog.Info("Database pool ready", "db", cfg.DB)

	api := application{
		config: cfg,
		pool:   pool,
	}

	slog.Info("Starting activity-library api", "version", version)

	return api.run(api.mount())
}
