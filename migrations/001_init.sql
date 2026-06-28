-- +goose Up
CREATE TABLE IF NOT EXISTS users (
    id bigserial PRIMARY KEY,
    email varchar(64) NOT NULL,
    password varchar(255) NOT NULL,
    nickname varchar(64),
    avatar varchar(255),
    created_at timestamptz,
    updated_at timestamptz
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email ON users (email);

CREATE TABLE IF NOT EXISTS groups (
    id bigserial PRIMARY KEY,
    name varchar(128) NOT NULL,
    owner_id bigint,
    created_at timestamptz,
    updated_at timestamptz
);

CREATE TABLE IF NOT EXISTS group_members (
    id bigserial PRIMARY KEY,
    group_id bigint NOT NULL,
    user_id bigint NOT NULL,
    role smallint NOT NULL DEFAULT 0,
    created_at timestamptz
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_group_user ON group_members (group_id, user_id);

CREATE TABLE IF NOT EXISTS group_join_requests (
    id bigserial PRIMARY KEY,
    group_id bigint NOT NULL,
    user_id bigint NOT NULL,
    status varchar(32) NOT NULL,
    reviewed_by bigint,
    reviewed_at timestamptz,
    created_at timestamptz,
    updated_at timestamptz
);

CREATE INDEX IF NOT EXISTS idx_group_join_requests_group_id ON group_join_requests (group_id);
CREATE INDEX IF NOT EXISTS idx_group_join_requests_user_id ON group_join_requests (user_id);
CREATE INDEX IF NOT EXISTS idx_group_join_requests_reviewed_by ON group_join_requests (reviewed_by);

CREATE TABLE IF NOT EXISTS conversations (
    id bigserial PRIMARY KEY,
    type smallint NOT NULL,
    group_id bigint,
    created_at timestamptz,
    updated_at timestamptz
);

CREATE INDEX IF NOT EXISTS idx_conversations_group_id ON conversations (group_id);

CREATE TABLE IF NOT EXISTS conversation_members (
    id bigserial PRIMARY KEY,
    conversation_id bigint NOT NULL,
    user_id bigint NOT NULL,
    last_read_message_id bigint NOT NULL DEFAULT 0,
    created_at timestamptz
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_conv_user ON conversation_members (conversation_id, user_id);

CREATE TABLE IF NOT EXISTS messages (
    id bigserial PRIMARY KEY,
    conversation_id bigint NOT NULL,
    sender_id bigint NOT NULL,
    content text NOT NULL,
    created_at timestamptz
);

CREATE INDEX IF NOT EXISTS idx_messages_conversation_id ON messages (conversation_id);
CREATE INDEX IF NOT EXISTS idx_messages_sender_id ON messages (sender_id);

CREATE TABLE IF NOT EXISTS files (
    id bigserial PRIMARY KEY,
    user_id bigint NOT NULL,
    conversation_id bigint,
    original_name varchar(255) NOT NULL,
    stored_name varchar(255) NOT NULL,
    storage_key varchar(512) NOT NULL,
    url varchar(512) NOT NULL,
    content_type varchar(128) NOT NULL,
    size bigint NOT NULL,
    sha256 varchar(64) NOT NULL DEFAULT '',
    purpose varchar(32) NOT NULL,
    created_at timestamptz,
    deleted_at timestamptz
);

CREATE INDEX IF NOT EXISTS idx_files_user_id ON files (user_id);
CREATE INDEX IF NOT EXISTS idx_files_conversation_id ON files (conversation_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_files_storage_key ON files (storage_key);
CREATE INDEX IF NOT EXISTS idx_files_purpose ON files (purpose);
CREATE INDEX IF NOT EXISTS idx_files_deleted_at ON files (deleted_at);

CREATE TABLE IF NOT EXISTS upload_sessions (
    id bigserial PRIMARY KEY,
    upload_id varchar(64) NOT NULL,
    user_id bigint NOT NULL,
    original_name varchar(255) NOT NULL,
    content_type varchar(128) NOT NULL,
    size bigint NOT NULL,
    purpose varchar(32) NOT NULL,
    chunk_size bigint NOT NULL,
    total_chunks bigint NOT NULL,
    sha256 varchar(64) NOT NULL DEFAULT '',
    status varchar(32) NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz,
    updated_at timestamptz
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_upload_sessions_upload_id ON upload_sessions (upload_id);
CREATE INDEX IF NOT EXISTS idx_upload_sessions_user_id ON upload_sessions (user_id);
CREATE INDEX IF NOT EXISTS idx_upload_sessions_purpose ON upload_sessions (purpose);
CREATE INDEX IF NOT EXISTS idx_upload_sessions_status ON upload_sessions (status);
CREATE INDEX IF NOT EXISTS idx_upload_sessions_expires_at ON upload_sessions (expires_at);

CREATE TABLE IF NOT EXISTS upload_chunks (
    id bigserial PRIMARY KEY,
    upload_id varchar(64) NOT NULL,
    index bigint NOT NULL,
    size bigint NOT NULL,
    created_at timestamptz,
    updated_at timestamptz
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_upload_chunk ON upload_chunks (upload_id, index);

CREATE TABLE IF NOT EXISTS friend_relations (
    id bigserial PRIMARY KEY,
    user_id bigint NOT NULL,
    friend_id bigint NOT NULL,
    user_low_id bigint NOT NULL,
    user_high_id bigint NOT NULL,
    status varchar(32) NOT NULL,
    created_at timestamptz,
    updated_at timestamptz
);

CREATE INDEX IF NOT EXISTS idx_friend_relations_user_id ON friend_relations (user_id);
CREATE INDEX IF NOT EXISTS idx_friend_relations_friend_id ON friend_relations (friend_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_friend_pair ON friend_relations (user_low_id, user_high_id);

-- +goose Down
DROP TABLE IF EXISTS friend_relations;
DROP TABLE IF EXISTS upload_chunks;
DROP TABLE IF EXISTS upload_sessions;
DROP TABLE IF EXISTS files;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS conversation_members;
DROP TABLE IF EXISTS conversations;
DROP TABLE IF EXISTS group_join_requests;
DROP TABLE IF EXISTS group_members;
DROP TABLE IF EXISTS groups;
DROP TABLE IF EXISTS users;
