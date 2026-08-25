package main

import (
	"log/slog"

	"github.com/caarlos0/env/v11"
)

type config struct {
	Addr string   `env:"ADDR" envDefault:":8080"`
	DB   dbConfig `                              envPrefix:"DB_"`
}

type dbConfig struct {
	DSN      string `env:"DSN,required"`
	MaxConns int32  `env:"MAX_CONNS"    envDefault:"25"`
	MinConns int32  `env:"MIN_CONNS"    envDefault:"5"`
}

// loadConfig reads the process environment only. Populating that environment
// from a .env file is a development concern handled once, at startup, in main.
func loadConfig() (config, error) {
	var cfg config
	err := env.Parse(&cfg)
	return cfg, err
}

func (c dbConfig) LogValue() slog.Value {
	dsn := "[redacted]"
	if c.DSN == "" {
		dsn = "unset"
	}

	return slog.GroupValue(
		slog.String("dsn", dsn),
		slog.Int64("max_conns", int64(c.MaxConns)),
		slog.Int64("min_conns", int64(c.MinConns)),
	)
}
