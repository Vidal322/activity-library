package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Seed inserts the development dataset in seed_data.go.
//
// Every statement uses ON CONFLICT DO NOTHING against fixed IDs, so running it
// twice changes nothing and running it against a partially seeded database
// fills in the gaps. It does not update rows that already exist: to pick up an
// edit to the sample data, reset the database and seed again.
//
// The whole set goes in one transaction. A seed that half-applies because of a
// constraint violation leaves the database in a state nobody designed.
func Seed(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	// No-op once Commit has succeeded; on any early return it undoes the batch.
	defer tx.Rollback(ctx)

	batch := &pgx.Batch{}

	for _, u := range seedUsers {
		batch.Queue(`INSERT INTO users (id, name, email, pass_hash, img, role)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING`,
			u.ID, u.Name, u.Email, seedPassHash, u.Img, u.Role)
	}

	for _, f := range seedFamilies {
		batch.Queue(`INSERT INTO category_families (id, name, display_order)
			VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			f.ID, f.Name, f.DisplayOrder)
	}

	for _, c := range seedCategories {
		batch.Queue(`INSERT INTO categories (id, family_id, name, description, active, display_order)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING`,
			c.ID, c.FamilyID, c.Name, c.Description, c.Active, c.DisplayOrder)
	}

	for _, l := range seedLocations {
		batch.Queue(`INSERT INTO locations (id, name) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			l.ID, l.Name)
	}

	for _, m := range seedMaterials {
		batch.Queue(`INSERT INTO materials (id, name, description)
			VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			m.ID, m.Name, m.Description)
	}

	// Games before their join rows, and originals before variants. Statements
	// in a batch execute in the order queued, so seedGames must list an
	// original ahead of anything that references it.
	for _, g := range seedGames {
		batch.Queue(`INSERT INTO games (
				id, title, description, image, min_participants, max_participants,
				duration_min, duration_max, no_materials, publish_state,
				original_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			ON CONFLICT DO NOTHING`,
			g.ID, g.Title, g.Description, g.Image,
			g.MinParticipants, g.MaxParticipants, g.DurationMin, g.DurationMax,
			g.NoMaterials, g.PublishState, g.OriginalID)
	}

	for _, g := range seedGames {
		for _, authorID := range g.Authors {
			batch.Queue(`INSERT INTO game_authors (game_id, user_id)
				VALUES ($1, $2) ON CONFLICT DO NOTHING`, g.ID, authorID)
		}
		for _, categoryID := range g.Categories {
			batch.Queue(`INSERT INTO game_categories (game_id, category_id)
				VALUES ($1, $2) ON CONFLICT DO NOTHING`, g.ID, categoryID)
		}
		for _, locationID := range g.Locations {
			batch.Queue(`INSERT INTO game_locations (game_id, location_id)
				VALUES ($1, $2) ON CONFLICT DO NOTHING`, g.ID, locationID)
		}
		for _, inspirationID := range g.Inspirations {
			batch.Queue(`INSERT INTO game_inspirations (game_id, inspiration_id)
				VALUES ($1, $2) ON CONFLICT DO NOTHING`, g.ID, inspirationID)
		}
		for _, m := range g.Materials {
			batch.Queue(`INSERT INTO game_materials
					(game_id, material_id, quantity_base, quantity_per_participant, optional)
				VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`,
				g.ID, m.MaterialID, m.QuantityBase, m.QuantityPerParticipant, m.Optional)
		}
		for _, b := range g.Blocks {
			// The arbiter is named here, unlike everywhere else: an untargeted
			// ON CONFLICT considers every unique index on the table, and
			// blocks_game_position_key is deferrable, which Postgres rejects
			// outright as an arbiter. Naming the primary key sidesteps it.
			batch.Queue(`INSERT INTO blocks (id, game_id, type, content, position)
				VALUES ($1, $2, $3, $4, $5) ON CONFLICT (id) DO NOTHING`,
				b.ID, g.ID, b.Type, b.Content, b.Position)
		}
	}

	// Close reports the first statement that failed, so there is no need to
	// walk the results individually.
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("insert seed data: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

// seedPassHash is a bcrypt hash of "password". Sample accounts need something
// that parses as a hash; nothing here should ever reach a real deployment.
const seedPassHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
