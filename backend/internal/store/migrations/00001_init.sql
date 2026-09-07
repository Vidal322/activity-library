-- +goose Up

-- +goose StatementBegin
CREATE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TABLE users (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    name       text        NOT NULL CHECK (name <> ''),
    email      text        NOT NULL CHECK (email <> ''),
    pass_hash  text        NOT NULL,
    img        text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX users_email_key ON users (lower(email));

CREATE TRIGGER users_set_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE category_families (
    id            uuid        PRIMARY KEY DEFAULT uuidv7(),
    name          text        NOT NULL UNIQUE CHECK (name <> ''),
    display_order integer     NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER category_families_set_updated_at
    BEFORE UPDATE ON category_families
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE categories (
    id            uuid        PRIMARY KEY DEFAULT uuidv7(),
    family_id     uuid        NOT NULL REFERENCES category_families (id) ON DELETE RESTRICT,
    name          text        NOT NULL CHECK (name <> ''),
    description   text        NOT NULL DEFAULT '',
    active        boolean     NOT NULL DEFAULT true,
    display_order integer     NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT categories_family_name_key UNIQUE (family_id, name)
);

CREATE TRIGGER categories_set_updated_at
    BEFORE UPDATE ON categories
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE locations (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    name       text        NOT NULL UNIQUE CHECK (name <> ''),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER locations_set_updated_at
    BEFORE UPDATE ON locations
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE materials (
    id          uuid        PRIMARY KEY DEFAULT uuidv7(),
    name        text        NOT NULL UNIQUE CHECK (name <> ''),
    description text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER materials_set_updated_at
    BEFORE UPDATE ON materials
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

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
CREATE INDEX games_published_keyset_idx
    ON games (created_at DESC, id DESC)
    WHERE publish_state = 'published';

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
    game_id                  uuid        NOT NULL,
    material_id              uuid        NOT NULL REFERENCES materials (id) ON DELETE RESTRICT,
    quantity_base            integer     NOT NULL,
    quantity_per_participant integer     NOT NULL,
    optional                 boolean     NOT NULL DEFAULT false,
    game_no_materials        boolean     NOT NULL DEFAULT false CHECK (game_no_materials = false),
    created_at               timestamptz NOT NULL DEFAULT now(),
    updated_at               timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (game_id, material_id),
    CONSTRAINT game_materials_per_participant_not_negative
        CHECK (quantity_per_participant >= 0),
    CONSTRAINT game_materials_quantity_positive
        CHECK (quantity_base > 0 OR quantity_per_participant > 0),
    CONSTRAINT game_materials_game_fkey
        FOREIGN KEY (game_id, game_no_materials)
        REFERENCES games (id, no_materials) ON DELETE CASCADE
);

CREATE INDEX game_materials_material_idx ON game_materials (material_id);

CREATE TRIGGER game_materials_set_updated_at
    BEFORE UPDATE ON game_materials
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE EXTENSION IF NOT EXISTS unaccent;

CREATE TEXT SEARCH CONFIGURATION pt_unaccent (COPY = portuguese);

ALTER TEXT SEARCH CONFIGURATION pt_unaccent
    ALTER MAPPING FOR hword, hword_part, word
    WITH unaccent, portuguese_stem;

ALTER TABLE blocks
    ADD COLUMN search tsvector
        GENERATED ALWAYS AS (to_tsvector('pt_unaccent', content)) STORED;

CREATE INDEX blocks_search_idx ON blocks USING gin (search);

CREATE TABLE game_search (
    game_id  uuid     PRIMARY KEY REFERENCES games (id) ON DELETE CASCADE,
    document tsvector NOT NULL
);

CREATE INDEX game_search_document_idx ON game_search USING gin (document);

-- Weights: A title, B description and headings, C body copy. Image blocks are
-- skipped because their content is a path, not prose. Aggregating in position
-- order keeps the lexeme positions meaningful for ts_rank_cd's proximity term.
-- +goose StatementBegin
CREATE FUNCTION refresh_game_search(p_game_id uuid) RETURNS void
LANGUAGE sql AS $$
    INSERT INTO game_search (game_id, document)
    SELECT g.id,
           setweight(to_tsvector('pt_unaccent', g.title), 'A')
        || setweight(to_tsvector('pt_unaccent', g.description), 'B')
        || setweight(to_tsvector('pt_unaccent', coalesce(
               string_agg(b.content, ' ' ORDER BY b.position)
                   FILTER (WHERE b.type = 'heading'), '')), 'B')
        || setweight(to_tsvector('pt_unaccent', coalesce(
               string_agg(b.content, ' ' ORDER BY b.position)
                   FILTER (WHERE b.type NOT IN ('heading', 'image')), '')), 'C')
    FROM games g
    LEFT JOIN blocks b ON b.game_id = g.id
    WHERE g.id = p_game_id
    GROUP BY g.id
    ON CONFLICT (game_id) DO UPDATE SET document = EXCLUDED.document;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION games_refresh_search() RETURNS trigger AS $$
BEGIN
    PERFORM refresh_game_search(NEW.id);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER games_refresh_search
    AFTER INSERT OR UPDATE OF title, description ON games
    FOR EACH ROW EXECUTE FUNCTION games_refresh_search();

-- +goose StatementBegin
CREATE FUNCTION blocks_refresh_search() RETURNS trigger AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        PERFORM refresh_game_search(OLD.game_id);
    END IF;
    IF TG_OP <> 'DELETE' THEN
        PERFORM refresh_game_search(NEW.game_id);
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER blocks_refresh_search
    AFTER INSERT OR DELETE OR UPDATE OF content, type, game_id ON blocks
    FOR EACH ROW EXECUTE FUNCTION blocks_refresh_search();

-- +goose Down

DROP TRIGGER blocks_refresh_search ON blocks;
DROP TRIGGER games_refresh_search ON games;
DROP FUNCTION blocks_refresh_search;
DROP FUNCTION games_refresh_search;
DROP FUNCTION refresh_game_search;
DROP TABLE game_search;
ALTER TABLE blocks DROP COLUMN search;
DROP TEXT SEARCH CONFIGURATION pt_unaccent;

DROP TABLE game_materials;
DROP TABLE blocks;

DROP TABLE game_inspirations;
DROP TABLE game_locations;
DROP TABLE game_categories;
DROP TABLE games;

DROP TABLE materials;
DROP TABLE locations;
DROP TABLE categories;
DROP TABLE category_families;
DROP TABLE users;
DROP FUNCTION set_updated_at;
