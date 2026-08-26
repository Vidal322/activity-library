-- +goose Up

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

SELECT refresh_game_search(id) FROM games;

-- +goose Down

DROP TRIGGER blocks_refresh_search ON blocks;
DROP TRIGGER games_refresh_search ON games;
DROP FUNCTION blocks_refresh_search;
DROP FUNCTION games_refresh_search;
DROP FUNCTION refresh_game_search;
DROP TABLE game_search;
ALTER TABLE blocks DROP COLUMN search;
DROP TEXT SEARCH CONFIGURATION pt_unaccent;
