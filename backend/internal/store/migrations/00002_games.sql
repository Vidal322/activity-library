-- +goose Up

CREATE TABLE games (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    title               text        NOT NULL CHECK (title <> ''),
    description         text        NOT NULL DEFAULT '',
    image               text,
    min_participants    integer     CHECK (min_participants > 0),
    max_participants    integer     CHECK (max_participants > 0),
    duration_min        integer     CHECK (duration_min > 0),
    duration_max        integer     CHECK (duration_max > 0),
    no_materials        boolean     NOT NULL DEFAULT false,
    publish_state       text        NOT NULL DEFAULT 'draft'
                                    CHECK (publish_state IN ('draft', 'published')),
    author_id           uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    original_id         uuid,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    is_variant          boolean GENERATED ALWAYS AS (original_id IS NOT NULL) STORED,
    original_is_variant boolean GENERATED ALWAYS AS (
        CASE WHEN original_id IS NULL THEN NULL::boolean ELSE false END
    ) STORED,

    CONSTRAINT games_participants_range CHECK (min_participants <= max_participants),
    CONSTRAINT games_duration_range CHECK (duration_min <= duration_max),
    CONSTRAINT games_non_variant_complete CHECK (
        original_id IS NOT NULL
        OR (    min_participants IS NOT NULL
            AND max_participants IS NOT NULL
            AND duration_min IS NOT NULL
            AND duration_max IS NOT NULL)
    ),

    CONSTRAINT games_id_is_variant_key UNIQUE (id, is_variant),
    CONSTRAINT games_id_no_materials_key UNIQUE (id, no_materials),
    CONSTRAINT games_original_not_variant_fkey
        FOREIGN KEY (original_id, original_is_variant)
        REFERENCES games (id, is_variant)
);

CREATE INDEX games_author_idx ON games (author_id);
CREATE INDEX games_original_idx ON games (original_id) WHERE original_id IS NOT NULL;

CREATE TRIGGER games_set_updated_at
    BEFORE UPDATE ON games
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE game_categories (
    game_id     uuid        NOT NULL REFERENCES games (id) ON DELETE CASCADE,
    category_id uuid        NOT NULL REFERENCES categories (id) ON DELETE RESTRICT,
    created_at  timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (game_id, category_id)
);

CREATE INDEX game_categories_category_idx ON game_categories (category_id);

CREATE TABLE game_locations (
    game_id     uuid        NOT NULL REFERENCES games (id) ON DELETE CASCADE,
    location_id uuid        NOT NULL REFERENCES locations (id) ON DELETE RESTRICT,
    created_at  timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (game_id, location_id)
);

CREATE INDEX game_locations_location_idx ON game_locations (location_id);

CREATE TABLE game_inspirations (
    game_id        uuid        NOT NULL REFERENCES games (id) ON DELETE CASCADE,
    inspiration_id uuid        NOT NULL REFERENCES games (id) ON DELETE CASCADE,
    created_at     timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (game_id, inspiration_id),
    CONSTRAINT game_inspirations_not_self CHECK (game_id <> inspiration_id)
);

CREATE INDEX game_inspirations_inspiration_idx ON game_inspirations (inspiration_id);

-- +goose Down

DROP TABLE game_inspirations;
DROP TABLE game_locations;
DROP TABLE game_categories;
DROP TABLE games;
