-- +goose Up

CREATE INDEX games_published_keyset_idx
    ON games (created_at DESC, id DESC)
    WHERE publish_state = 'published';

-- +goose Down

DROP INDEX games_published_keyset_idx;
