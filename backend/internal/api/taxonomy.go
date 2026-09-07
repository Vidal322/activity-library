package api

import (
	"log/slog"
	"net/http"

	"github.com/Vidal322/activity-library/internal/store"
)

type category struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	DisplayOrder int32  `json:"display_order"`
}

type categoryFamily struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	DisplayOrder int32      `json:"display_order"`
	Categories   []category `json:"categories"`
}

// categoriesResponse wraps the tree in an object
type categoriesResponse struct {
	Families []categoryFamily `json:"families"`
}

func newCategoriesResponse(families []store.CategoryFamily) categoriesResponse {
	out := make([]categoryFamily, 0, len(families))

	for _, f := range families {
		categories := make([]category, 0, len(f.Categories))
		for _, c := range f.Categories {
			categories = append(categories, category{
				ID:           c.ID,
				Name:         c.Name,
				Description:  c.Description,
				DisplayOrder: c.DisplayOrder,
			})
		}

		out = append(out, categoryFamily{
			ID:           f.ID,
			Name:         f.Name,
			DisplayOrder: f.DisplayOrder,
			Categories:   categories,
		})
	}

	return categoriesResponse{Families: out}
}

func (s *Server) handleCategoriesList(w http.ResponseWriter, r *http.Request) {
	families, err := store.ListCategories(r.Context(), s.pool)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	if err := writeJSON(w, http.StatusOK, newCategoriesResponse(families)); err != nil {
		slog.Error("Failed to write categories list response", "error", err)
	}
}
