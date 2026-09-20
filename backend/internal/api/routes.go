package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

const apiPrefix = "/v1"

func (s *Server) routes() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.ClientIPFromRemoteAddr)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Use(middleware.Timeout(handlerTimeout))

	r.Get("/health", s.handleHealth)
	r.Get("/readyz", s.handleReadiness)

	r.Route(apiPrefix, func(r chi.Router) {
		r.Post("/users", s.handleCreateUser)
		r.Route("/auth", func(r chi.Router) {
			r.With(s.requireAuth).Get("/me", s.handleMe)
			r.Post("/login", s.handleLogin)
			r.Post("/logout", s.handleLogout)
		})

		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)

			r.Get("/games", s.handleGamesList)
			r.Post("/games", s.handleCreateDraft)
			r.Get("/games/{id}", s.handleGetGame)
			r.Get("/categories", s.handleCategoriesList)
			r.Get("/locations", s.handleLocationsList)
			r.Get("/materials", s.handleMaterialsList)
			r.Get("/users/{id}", s.handleGetUser)
		})
	})

	return r
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("all good"))
}

func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()

	if err := s.pool.Ping(ctx); err != nil {
		slog.Error("Readiness check failed", "error", err)
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}

	w.Write([]byte("ready"))
}
