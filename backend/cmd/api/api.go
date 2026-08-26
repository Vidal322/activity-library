package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Vidal322/activity-library/internal/config"
)

const (
	readTimeout      = 10 * time.Second
	handlerTimeout   = 30 * time.Second
	writeTimeout     = handlerTimeout + 5*time.Second
	idleTimeout      = time.Minute
	shutdownTimeout  = 15 * time.Second
	readinessTimeout = 2 * time.Second
)

type application struct {
	config config.Config
	// logger
	pool *pgxpool.Pool
}

func (app *application) mount() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.ClientIPFromRemoteAddr)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Use(middleware.Timeout(handlerTimeout))

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("all good"))
	})

	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
		defer cancel()

		if err := app.pool.Ping(ctx); err != nil {
			slog.Error("Readiness check failed", "error", err)
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}

		w.Write([]byte("ready"))
	})

	return r
}

func (app *application) run(h http.Handler) error {
	srv := &http.Server{
		Addr:         app.config.Addr,
		Handler:      h,
		WriteTimeout: writeTimeout,
		ReadTimeout:  readTimeout,
		IdleTimeout:  idleTimeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	slog.Info("Server has started at addr", "address", app.config.Addr)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		stop() // restore default signal handling so a second Ctrl+C aborts immediately
		slog.Info("Server is shutting down", "timeout", shutdownTimeout)

		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}

		slog.Info("Server stopped gracefully")
		return nil
	}
}
