package main

import (
	"log/slog"
	"os"

	"github.com/Vidal322/activity-library/internal/api"
)

// version is stamped at link time via -X main.version.
var version = "dev"

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	if err := api.Run(version); err != nil {
		slog.Error("Fatal error", "error", err)
		os.Exit(1)
	}
}
