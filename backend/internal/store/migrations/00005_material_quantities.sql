-- +goose Up

ALTER TABLE game_materials
  ADD COLUMN quantity_base integer,
  ADD COLUMN quantity_per_participant integer;

UPDATE game_materials
SET quantity_base = CASE WHEN per_participant THEN 0 ELSE quantity END,
    quantity_per_participant = CASE WHEN per_participant THEN quantity ELSE 0 END;

ALTER TABLE game_materials
  ALTER COLUMN quantity_base SET NOT NULL,
  ALTER COLUMN quantity_per_participant SET NOT NULL,
  DROP COLUMN quantity,
  DROP COLUMN per_participant,
  ADD CONSTRAINT game_materials_per_participant_not_negative
    CHECK (quantity_per_participant >= 0),
  ADD CONSTRAINT game_materials_quantity_positive
    CHECK (quantity_base > 0 OR quantity_per_participant > 0);


-- +goose Down

ALTER TABLE game_materials
    ADD COLUMN quantity        integer NOT NULL DEFAULT 1 CHECK (quantity > 0),
    ADD COLUMN per_participant boolean NOT NULL DEFAULT false;

UPDATE game_materials
SET quantity        = GREATEST(quantity_base, quantity_per_participant),
    per_participant = quantity_per_participant > 0;

ALTER TABLE game_materials
    DROP COLUMN quantity_base,
    DROP COLUMN quantity_per_participant;
