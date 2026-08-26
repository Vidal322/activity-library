package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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

// Server carries the dependencies every handler shares. Handler methods hang
// off it, so adding a dependency widens this struct rather than every route.
type Server struct {
	config config.Config
	pool   *pgxpool.Pool
}

func NewServer(cfg config.Config, pool *pgxpool.Pool) *Server {
	return &Server{
		config: cfg,
		pool:   pool,
	}
}

func (s *Server) serve(h http.Handler) error {
	srv := &http.Server{
		Addr:         s.config.Addr,
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

	slog.Info("Server has started at addr", "address", s.config.Addr)

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
