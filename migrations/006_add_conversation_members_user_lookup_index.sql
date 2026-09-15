-- +goose Up
-- 私聊会话解析先按 user_id 过滤成员；原有 (conversation_id, user_id) 唯一索引
-- 不能高效覆盖这个方向。该索引也服务按用户列出所属会话的查询。
CREATE INDEX IF NOT EXISTS idx_conversation_members_user_conversation
ON conversation_members (user_id, conversation_id);

-- +goose Down
DROP INDEX IF EXISTS idx_conversation_members_user_conversation;
