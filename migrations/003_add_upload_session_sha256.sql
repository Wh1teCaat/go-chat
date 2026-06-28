-- +goose Up
ALTER TABLE upload_sessions ADD COLUMN IF NOT EXISTS sha256 varchar(64) NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE upload_sessions DROP COLUMN IF EXISTS sha256;
