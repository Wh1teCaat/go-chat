package database

import (
	"errors"
	"fmt"
	"strings"

	"chat_proj/internal/config"
	"chat_proj/pkg/logger"

	"github.com/pressly/goose/v3"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const defaultManagementDB = "postgres"
const defaultMigrationsDir = "migrations"

func InitDB(dbConfig config.DatabaseConfig) (*gorm.DB, error) {
	if strings.TrimSpace(dbConfig.DBName) == "" {
		err := errors.New("database name is required")
		logger.Error("Database name is required", logger.Any("error", err))
		return nil, err
	}

	managementDB, err := gorm.Open(postgres.Open(buildDSN(dbConfig, defaultManagementDB)), &gorm.Config{})
	if err != nil {
		logger.Error("Failed to connect to management database", logger.Any("error", err))
		return nil, err
	}

	if err := ensureAndCreate(managementDB, dbConfig.DBName); err != nil {
		logger.Error("Failed to ensure target database exists", logger.Any("error", err))
		return nil, err
	}

	db, err := gorm.Open(postgres.Open(buildDSN(dbConfig, dbConfig.DBName)), &gorm.Config{})
	if err != nil {
		logger.Error("Failed to connect to target database", logger.Any("error", err))
		return nil, err
	}

	if err := runMigrations(db, defaultMigrationsDir); err != nil {
		logger.Error("Failed to migrate database schema", logger.Any("error", err))
		return nil, err
	}

	return db, nil
}

func runMigrations(db *gorm.DB, dir string) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(sqlDB, dir)
}

func ensureAndCreate(db *gorm.DB, dbName string) error {
	var exists bool
	query := `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = ?)`
	if err := db.Raw(query, dbName).Scan(&exists).Error; err != nil {
		return err
	}

	if exists {
		return nil
	}

	return db.Exec(fmt.Sprintf("CREATE DATABASE %s", dbName)).Error
}

func buildDSN(dbConfig config.DatabaseConfig, dbName string) string {
	return fmt.Sprintf(
		"host=%s user=%s password=%s dbname=%s port=%d sslmode=%s TimeZone=%s",
		dbConfig.Host,
		dbConfig.User,
		dbConfig.Password,
		dbName,
		dbConfig.Port,
		dbConfig.SSLMode,
		dbConfig.TimeZone,
	)
}
