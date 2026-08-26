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

-- +goose Down

DROP TABLE materials;
DROP TABLE locations;
DROP TABLE categories;
DROP TABLE category_families;
DROP TABLE users;
DROP FUNCTION set_updated_at;
