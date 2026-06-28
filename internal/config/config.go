package config

import (
	"chat_proj/pkg/logger"
	"fmt"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	Log       LogConfig       `mapstructure:"log"`
	Server    ServerConfig    `mapstructure:"server"`
	Database  DatabaseConfig  `mapstructure:"database"`
	JWT       JWTConfig       `mapstructure:"jwt"`
	CORS      CORSConfig      `mapstructure:"cors"`
	Redis     RedisConfig     `mapstructure:"redis"`
	RateLimit RateLimitConfig `mapstructure:"rate_limit"`
}

type LogConfig struct {
	Path  string `mapstructure:"path"`
	Level string `mapstructure:"level"`
}

type ServerConfig struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}

func (s ServerConfig) Address() string {
	return fmt.Sprintf("%s:%d", s.Host, s.Port)
}

type DatabaseConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
	DBName   string `mapstructure:"dbname"`
	SSLMode  string `mapstructure:"sslmode"`
	TimeZone string `mapstructure:"timezone"`
}

type JWTConfig struct {
	Secret string `mapstructure:"secret"`
}

type CORSConfig struct {
	AllowedOrigins []string `mapstructure:"allowed_origins"`
}

type RedisConfig struct {
	Enabled  bool   `mapstructure:"enabled"`
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

type RateLimitConfig struct {
	Enabled       bool `mapstructure:"enabled"`
	Requests      int  `mapstructure:"requests"`
	WindowSeconds int  `mapstructure:"window_seconds"`
}

func (r RateLimitConfig) Window() time.Duration {
	if r.WindowSeconds <= 0 {
		return time.Minute
	}
	return time.Duration(r.WindowSeconds) * time.Second
}

func (r RateLimitConfig) Limit() int {
	if r.Requests <= 0 {
		return 120
	}
	return r.Requests
}

func Load() (*Config, error) {
	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("toml")
	v.AddConfigPath("./configs")
	v.AddConfigPath("../configs")
	v.AddConfigPath("../../configs")

	v.SetDefault("log.path", "logs/app.log")
	v.SetDefault("log.level", "info")
	v.SetDefault("server.host", "0.0.0.0")
	v.SetDefault("server.port", 8080)
	v.SetDefault("database.host", "127.0.0.1")
	v.SetDefault("database.port", 5432)
	v.SetDefault("database.user", "postgres")
	v.SetDefault("database.password", "postgres")
	v.SetDefault("database.dbname", "chat_proj")
	v.SetDefault("database.sslmode", "disable")
	v.SetDefault("database.timezone", "Asia/Shanghai")
	v.SetDefault("jwt.secret", "change-me")
	v.SetDefault("cors.allowed_origins", DefaultCORSAllowedOrigins())
	v.SetDefault("redis.enabled", false)
	v.SetDefault("redis.addr", "127.0.0.1:6379")
	v.SetDefault("redis.password", "")
	v.SetDefault("redis.db", 0)
	v.SetDefault("rate_limit.enabled", true)
	v.SetDefault("rate_limit.requests", 120)
	v.SetDefault("rate_limit.window_seconds", 60)

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			logger.Error("Failed to read config file", logger.Any("error", err))
			return nil, err
		}
		logger.Warn("Config file not found, using defaults")
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		logger.Error("Failed to unmarshal config", logger.Any("error", err))
		return nil, err
	}
	return &cfg, nil
}

func DefaultCORSAllowedOrigins() []string {
	return []string{
		"http://localhost:3000",
		"http://localhost:5173",
		"http://127.0.0.1:3000",
		"http://127.0.0.1:5173",
	}
}
