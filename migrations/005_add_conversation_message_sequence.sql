-- +goose Up
-- 会话序号用于跨实例消息的权威顺序。全局 messages.id 不能代表并发会话的发布顺序。
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS last_seq bigint NOT NULL DEFAULT 0;
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS last_published_seq bigint NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS seq bigint NOT NULL DEFAULT 0;

-- 旧数据按原会话内 ID 回填。部署期间应先停止旧版本写入，避免回填与新写并发。
WITH numbered AS (
    SELECT id, row_number() OVER (PARTITION BY conversation_id ORDER BY id) AS sequence
    FROM messages
)
UPDATE messages
SET seq = numbered.sequence
FROM numbered
WHERE messages.id = numbered.id AND messages.seq = 0;

UPDATE conversations c
SET last_seq = COALESCE((SELECT MAX(m.seq) FROM messages m WHERE m.conversation_id = c.id), 0),
    last_published_seq = COALESCE((SELECT MAX(m.seq) FROM messages m WHERE m.conversation_id = c.id), 0);

ALTER TABLE messages ADD CONSTRAINT uq_messages_conversation_seq UNIQUE (conversation_id, seq);

-- +goose Down
ALTER TABLE messages DROP CONSTRAINT IF EXISTS uq_messages_conversation_seq;
ALTER TABLE messages DROP COLUMN IF EXISTS seq;
ALTER TABLE conversations DROP COLUMN IF EXISTS last_published_seq;
ALTER TABLE conversations DROP COLUMN IF EXISTS last_seq;
