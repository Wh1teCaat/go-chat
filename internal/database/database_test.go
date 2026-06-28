package database

import (
	"os"
	"strings"
	"testing"

	"chat_proj/internal/config"
)

func TestBuildDSNUsesConfiguredDatabase(t *testing.T) {
	dsn := buildDSN(testDBConfig(), "chat_proj")

	if !strings.Contains(dsn, "dbname=chat_proj") {
		t.Fatalf("expected target dbname in dsn, got %q", dsn)
	}
}

func TestInitialMigrationUsesGooseDirectivesAndCoreIndexes(t *testing.T) {
	content, err := os.ReadFile("../../migrations/001_init.sql")
	if err != nil {
		t.Fatalf("failed to read migration: %v", err)
	}
	sql := string(content)

	for _, want := range []string{
		"-- +goose Up",
		"-- +goose Down",
		"CREATE TABLE IF NOT EXISTS group_join_requests",
		"sha256 varchar(64) NOT NULL DEFAULT ''",
		"CREATE TABLE IF NOT EXISTS upload_sessions",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_friend_pair",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("expected migration to contain %q", want)
		}
	}
}

func TestUploadSessionSHA256MigrationUsesGooseDirectives(t *testing.T) {
	content, err := os.ReadFile("../../migrations/003_add_upload_session_sha256.sql")
	if err != nil {
		t.Fatalf("failed to read migration: %v", err)
	}
	sql := string(content)

	for _, want := range []string{
		"-- +goose Up",
		"ALTER TABLE upload_sessions ADD COLUMN IF NOT EXISTS sha256 varchar(64) NOT NULL DEFAULT ''",
		"-- +goose Down",
		"ALTER TABLE upload_sessions DROP COLUMN IF EXISTS sha256",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("expected migration to contain %q", want)
		}
	}
}

func testDBConfig() config.DatabaseConfig {
	return config.DatabaseConfig{
		Host:     "127.0.0.1",
		Port:     5432,
		User:     "postgres",
		Password: "postgres",
		DBName:   "chat_proj",
		SSLMode:  "disable",
		TimeZone: "Asia/Shanghai",
	}
}
