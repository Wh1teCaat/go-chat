-- +goose Up
-- The message transaction updates conversations.last_seq while the asynchronous
-- publisher updates this separate row. This removes unnecessary lock contention
-- between persistence and real-time delivery confirmation.
CREATE TABLE IF NOT EXISTS conversation_publish_watermarks (
    conversation_id bigint PRIMARY KEY REFERENCES conversations(id) ON DELETE CASCADE,
    last_published_seq bigint NOT NULL DEFAULT 0
);

INSERT INTO conversation_publish_watermarks (conversation_id, last_published_seq)
SELECT id, last_published_seq FROM conversations
ON CONFLICT (conversation_id) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS conversation_publish_watermarks;
