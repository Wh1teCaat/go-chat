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
	Kafka     KafkaConfig     `mapstructure:"kafka"`
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

// Address 返回 HTTP 服务监听地址。
func (s ServerConfig) Address() string {
	return fmt.Sprintf("%s:%d", s.Host, s.Port)
}

type DatabaseConfig struct {
	Host         string `mapstructure:"host"`
	Port         int    `mapstructure:"port"`
	User         string `mapstructure:"user"`
	Password     string `mapstructure:"password"`
	DBName       string `mapstructure:"dbname"`
	SSLMode      string `mapstructure:"sslmode"`
	TimeZone     string `mapstructure:"timezone"`
	MaxOpenConns int    `mapstructure:"max_open_conns"`
	// MessageAsyncCommit 仅让聊天消息事务不等待 WAL fsync；默认关闭，优先持久性。
	MessageAsyncCommit bool `mapstructure:"message_async_commit"`
}

// PoolMaxOpen 返回数据库连接池的最大打开连接数，并提供安全默认值。
func (d DatabaseConfig) PoolMaxOpen() int {
	if d.MaxOpenConns <= 0 {
		return 30
	}
	return d.MaxOpenConns
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

type KafkaConfig struct {
	Enabled         bool     `mapstructure:"enabled"`
	Brokers         []string `mapstructure:"brokers"`
	Topic           string   `mapstructure:"topic"`
	GroupID         string   `mapstructure:"group_id"`
	ClientID        string   `mapstructure:"client_id"`
	ConsumerWorkers int      `mapstructure:"consumer_workers"`
}

type RateLimitConfig struct {
	Enabled       bool `mapstructure:"enabled"`
	Requests      int  `mapstructure:"requests"`
	WindowSeconds int  `mapstructure:"window_seconds"`
}

// Window 返回限流统计窗口，并在配置无效时使用默认值。
func (r RateLimitConfig) Window() time.Duration {
	if r.WindowSeconds <= 0 {
		return time.Minute
	}
	return time.Duration(r.WindowSeconds) * time.Second
}

// Limit 返回单个限流窗口允许的请求数，并在配置无效时使用默认值。
func (r RateLimitConfig) Limit() int {
	if r.Requests <= 0 {
		return 120
	}
	return r.Requests
}

// Load 从配置文件和环境变量加载并校验应用配置。
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
	v.SetDefault("database.max_open_conns", 30)
	v.SetDefault("database.message_async_commit", false)
	v.SetDefault("jwt.secret", "change-me")
	v.SetDefault("cors.allowed_origins", DefaultCORSAllowedOrigins())
	v.SetDefault("redis.enabled", true)
	v.SetDefault("redis.addr", "127.0.0.1:6379")
	v.SetDefault("redis.password", "")
	v.SetDefault("redis.db", 0)
	// 聊天消息默认经 Kafka 异步接收和按会话分区处理；需要简化部署时可在
	// 配置文件中显式设为 false，退回同步 PostgreSQL 写入路径。
	v.SetDefault("kafka.enabled", true)
	v.SetDefault("kafka.brokers", []string{"127.0.0.1:9092"})
	v.SetDefault("kafka.topic", "chat-messages")
	v.SetDefault("kafka.group_id", "go-chat-message-writers")
	v.SetDefault("kafka.client_id", "go-chat")
	v.SetDefault("kafka.consumer_workers", 4)
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

// DefaultCORSAllowedOrigins 返回开发环境使用的默认跨域来源列表。
func DefaultCORSAllowedOrigins() []string {
	return []string{
		"http://localhost:3000",
		"http://localhost:5173",
		"http://127.0.0.1:3000",
		"http://127.0.0.1:5173",
	}
}
