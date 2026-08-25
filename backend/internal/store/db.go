package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	maxConnIdleTime       = 15 * time.Minute
	maxConnLifetime       = time.Hour
	maxConnLifetimeJitter = 5 * time.Minute
	healthCheckPeriod     = 30 * time.Second
)

// PoolConfig carries the pool settings that differ between environments. The
// rest are the constants above: operational defaults, not deployment knobs.
type PoolConfig struct {
	DSN      string
	MaxConns int32
	MinConns int32
}

// InitPool opens the connection pool and verifies the database is reachable.
// ctx bounds startup only; it is not retained for the pool's lifetime, and
// queries later run under their own request context.
func InitPool(ctx context.Context, cfg PoolConfig) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		// pgx quotes the connection string back in this error, password
		// included, so the underlying error must not reach a log.
		return nil, errors.New("invalid database DSN")
	}

	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnIdleTime = maxConnIdleTime
	poolCfg.MaxConnLifetime = maxConnLifetime
	// Without jitter the connections opened together at startup expire
	// together, reconnecting in a burst.
	poolCfg.MaxConnLifetimeJitter = maxConnLifetimeJitter
	poolCfg.HealthCheckPeriod = healthCheckPeriod

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	// NewWithConfig does not itself connect, so ping to fail at startup on an
	// unreachable or misconfigured database rather than on the first request.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return pool, nil
}
