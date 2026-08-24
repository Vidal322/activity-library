package main

import (
	"log/slog"
	"os"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)
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
