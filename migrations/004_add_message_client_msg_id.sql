-- +goose Up
-- client_msg_id 允许为 NULL：历史消息和不带 clientMsgID 的消息不参与唯一约束。
ALTER TABLE messages ADD COLUMN IF NOT EXISTS client_msg_id varchar(64);
CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_sender_client_msg ON messages (sender_id, client_msg_id) WHERE client_msg_id IS NOT NULL;
-- 会话内按 id 翻页/取最新消息的覆盖索引，替代单列 conversation_id 索引的场景。
CREATE INDEX IF NOT EXISTS idx_messages_conversation_id_id ON messages (conversation_id, id DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_messages_conversation_id_id;
DROP INDEX IF EXISTS idx_messages_sender_client_msg;
ALTER TABLE messages DROP COLUMN IF EXISTS client_msg_id;
