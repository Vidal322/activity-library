package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/joho/godotenv"

	"github.com/Vidal322/activity-library/internal/config"
	"github.com/Vidal322/activity-library/internal/store"
)

const (
	dbStartupTimeout = 10 * time.Second
	migrationTimeout = 20 * time.Second
	seedTimeout      = 30 * time.Second
)

var localHosts = []string{"localhost", "127.0.0.1", "::1", "postgres"}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	if err := run(); err != nil {
		slog.Error("Fatal error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	force := flag.Bool("force", false,
		"seed a database that is not on localhost (this writes sample data to a shared database)")
	flag.Parse()

	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read .env: %w", err)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if err := checkLocal(cfg.DB.DSN, *force); err != nil {
		return err
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

	migrateCtx, cancel := context.WithTimeout(context.Background(), migrationTimeout)
	defer cancel()

	if err := store.Migrate(migrateCtx, pool); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	seedCtx, cancel := context.WithTimeout(context.Background(), seedTimeout)
	defer cancel()

	if err := store.Seed(seedCtx, pool); err != nil {
		return fmt.Errorf("seed: %w", err)
	}

	slog.Info("Seed complete")

	return nil
}

func checkLocal(dsn string, force bool) error {
	pgCfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return errors.New("invalid database DSN")
	}

	if force || slices.Contains(localHosts, pgCfg.Host) {
		return nil
	}

	return fmt.Errorf("refusing to seed database host %q: pass -force to override", pgCfg.Host)
}
