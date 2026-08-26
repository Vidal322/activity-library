-- +goose Up

-- The unique constraint is deferred so a reorder can pass through duplicate
-- positions mid-transaction and only has to hold at COMMIT. 
CREATE TABLE blocks (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    game_id    uuid        NOT NULL REFERENCES games (id) ON DELETE CASCADE,
    type       text        NOT NULL CHECK (type IN (
                               'paragraph', 'steps', 'bullet_points',
                               'table', 'heading', 'note', 'image')),
    content    text        NOT NULL DEFAULT '',
    position   integer     NOT NULL CHECK (position >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT blocks_game_position_key UNIQUE (game_id, position)
        DEFERRABLE INITIALLY DEFERRED
);

CREATE TRIGGER blocks_set_updated_at
    BEFORE UPDATE ON blocks
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Same composite-FK pattern as games_original_not_variant_fkey in 00002:
-- game_no_materials is pinned to false and paired with game_id, so a row here
-- can only reference a game whose no_materials is false. 
CREATE TABLE game_materials (
    game_id           uuid        NOT NULL,
    material_id       uuid        NOT NULL REFERENCES materials (id) ON DELETE RESTRICT,
    quantity          integer     NOT NULL DEFAULT 1 CHECK (quantity > 0),
    per_participant   boolean     NOT NULL DEFAULT false,
    optional          boolean     NOT NULL DEFAULT false,
    game_no_materials boolean     NOT NULL DEFAULT false CHECK (game_no_materials = false),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (game_id, material_id),
    CONSTRAINT game_materials_game_fkey
        FOREIGN KEY (game_id, game_no_materials)
        REFERENCES games (id, no_materials) ON DELETE CASCADE
);

CREATE INDEX game_materials_material_idx ON game_materials (material_id);

CREATE TRIGGER game_materials_set_updated_at
    BEFORE UPDATE ON game_materials
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down

DROP TABLE game_materials;
DROP TABLE blocks;
