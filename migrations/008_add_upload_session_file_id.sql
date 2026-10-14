-- +goose Up
ALTER TABLE upload_sessions ADD COLUMN IF NOT EXISTS file_id bigint;
CREATE INDEX IF NOT EXISTS idx_upload_sessions_file_id ON upload_sessions (file_id);

-- +goose Down
DROP INDEX IF EXISTS idx_upload_sessions_file_id;
ALTER TABLE upload_sessions DROP COLUMN IF EXISTS file_id;
