package main

import (
	"errors"
	"log/slog"
	"os"

	"github.com/joho/godotenv"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	slog.SetDefault(logger)

	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Error("Could not read .env", "error", err)
		os.Exit(1)
	}

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("Could not load config", "error", err)
		os.Exit(1)
	}

	api := application{
		config: cfg,
	}

	slog.Info("Starting activity-library api", "version", version)

	err = api.run(api.mount())
	if err != nil {
		slog.Error("Server failed to start", "error", err)
		os.Exit(1)
	}
}
