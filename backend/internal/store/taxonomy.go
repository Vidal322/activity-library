package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Category struct {
	ID           string `db:"id"`
	Name         string `db:"name"`
	Description  string `db:"description"`
	DisplayOrder int32  `db:"display_order"`
}

type CategoryFamily struct {
	ID           string
	Name         string
	DisplayOrder int32
	Categories   []Category
}

type categoryRow struct {
	FamilyID           string `db:"family_id"`
	FamilyName         string `db:"family_name"`
	FamilyDisplayOrder int32  `db:"family_display_order"`
	ID                 string `db:"id"`
	Name               string `db:"name"`
	Description        string `db:"description"`
	DisplayOrder       int32  `db:"display_order"`
}

const listCategoriesQuery = `
	SELECT f.id AS family_id, f.name AS family_name,
	       f.display_order AS family_display_order,
	       c.id, c.name, c.description, c.display_order
	FROM categories c
	JOIN category_families f ON f.id = c.family_id
	WHERE c.active
	ORDER BY f.display_order, f.name, c.display_order, c.name`

func ListCategories(ctx context.Context, pool *pgxpool.Pool) ([]CategoryFamily, error) {
	rows, err := pool.Query(ctx, listCategoriesQuery)
	if err != nil {
		return nil, classify(err)
	}

	flat, err := pgx.CollectRows(rows, pgx.RowToStructByName[categoryRow])
	if err != nil {
		return nil, classify(err)
	}

	families := make([]CategoryFamily, 0)
	for _, r := range flat {
		if len(families) == 0 || families[len(families)-1].ID != r.FamilyID {
			families = append(families, CategoryFamily{
				ID:           r.FamilyID,
				Name:         r.FamilyName,
				DisplayOrder: r.FamilyDisplayOrder,
				Categories:   []Category{},
			})
		}

		f := &families[len(families)-1]
		f.Categories = append(f.Categories, Category{
			ID:           r.ID,
			Name:         r.Name,
			Description:  r.Description,
			DisplayOrder: r.DisplayOrder,
		})
	}

	return families, nil
}

// Locations are a flat, bounded list, so this returns every row rather than a
// page. The type comes from games.go: the columns are the same two.
const listLocationsQuery = `
	SELECT id, name
	FROM locations
	ORDER BY name`

func ListLocations(ctx context.Context, pool *pgxpool.Pool) ([]Location, error) {
	rows, err := pool.Query(ctx, listLocationsQuery)
	if err != nil {
		return nil, classify(err)
	}

	locations, err := pgx.CollectRows(rows, pgx.RowToStructByName[Location])
	if err != nil {
		return nil, classify(err)
	}

	return locations, nil
}
