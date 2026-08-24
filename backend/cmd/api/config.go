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
	DSN string `env:"DSN,required"`
}

// loadConfig reads the process environment only. Populating that environment
// from a .env file is a development concern handled once, at startup, in main.
func loadConfig() (config, error) {
	var cfg config
	err := env.Parse(&cfg)
	return cfg, err
}

// Prevent database config from leaking in logs
// Redacts entire dsn in logs
func (c dbConfig) LogValue() slog.Value {
	if c.DSN == "" {
		return slog.StringValue("unset")
	}
	return slog.StringValue("[redacted]")
}
