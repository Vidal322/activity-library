package main

import (
	"log/slog"
	"os"
)

var version = "dev"

func main() {
	cfg := config{
		addr: ":8080",
		db:   dbConfig{},
	}
	api := application{
		config: cfg,
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	slog.Info("Starting activity-library api", "version", version)

	err := api.run(api.mount())
	if err != nil {
		slog.Error("Server failed to start", "error", err)
		os.Exit(1)
	}
}
