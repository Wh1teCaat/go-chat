package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"chat_proj/internal/auth"
	"chat_proj/internal/cache"
	"chat_proj/internal/config"
	"chat_proj/internal/controller"
	"chat_proj/internal/database"
	"chat_proj/internal/presence"
	"chat_proj/internal/ratelimit"
	"chat_proj/internal/repository"
	"chat_proj/internal/router"
	"chat_proj/internal/service"
	"chat_proj/internal/storage"
	"chat_proj/pkg/logger"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type appDeps struct {
	loadConfig     func() (*config.Config, error)
	initAuth       func(string) error
	initDB         func(config.DatabaseConfig) (*gorm.DB, error)
	newRouter      func(*config.Config, ratelimit.Limiter) *gin.Engine
	listenAndServe func(*http.Server) error
	exit           func(int)
}

func main() {
	run(appDeps{
		loadConfig: config.Load,
		initAuth:   auth.Init,
		initDB:     database.InitDB,
		newRouter: func(cfg *config.Config, limiter ratelimit.Limiter) *gin.Engine {
			return router.NewWithConfigAndOptions(cfg, router.Options{RateLimiter: limiter})
		},
		listenAndServe: func(s *http.Server) error {
			return s.ListenAndServe()
		},
		exit: os.Exit,
	})
}

func run(deps appDeps) {
	// 配置加载失败前也需要有默认日志出口，避免启动问题完全静默。
	logger.InitLogger("logs/app.log", "info")

	cfg, err := deps.loadConfig()
	if err != nil {
		logger.Error("Failed to load config", logger.Any("error", err))
		deps.exit(1)
		return
	}
	logger.InitLogger(cfg.Log.Path, cfg.Log.Level)
	logger.Info("Config loaded successfully")

	if err := deps.initAuth(cfg.JWT.Secret); err != nil {
		logger.Error("Failed to initialize auth", logger.Any("error", err))
		deps.exit(1)
		return
	}
	logger.Info("Auth initialized successfully")

	db, err := deps.initDB(cfg.Database)
	if err != nil {
		logger.Error("Failed to initialize database", logger.Any("error", err))
		deps.exit(1)
		return
	}
	logger.Info("Database initialized successfully")

	service.Init(repository.NewRepository(db))
	// 当前使用本地文件系统保存上传文件；后续换 OSS/MinIO 只需要替换 storage 实现。
	service.InitFileStorage(storage.NewLocalStorage("uploads", "/uploads"))
	startMultipartUploadCleanup(context.Background(), time.Hour)

	redisClient, err := cache.NewRedisClient(context.Background(), cfg.Redis)
	if err != nil {
		logger.Error("Failed to initialize redis", logger.Any("error", err))
		deps.exit(1)
		return
	}
	if redisClient != nil {
		logger.Info("Redis initialized successfully")
		service.InitCacheStore(cache.NewRedisStore(redisClient))
		defer redisClient.Close()
	} else {
		service.InitCacheStore(nil)
	}
	presenceStore := initPresenceStore(redisClient)
	service.InitPresenceStore(presenceStore)
	controller.InitPresenceStore(presenceStore)
	limiter := initRateLimiter(redisClient)

	if err := deps.listenAndServe(buildServer(cfg, deps.newRouter(cfg, limiter))); err != nil {
		logger.Error("Server error", logger.Any("error", err))
		deps.exit(1)
	}
}

func initRateLimiter(redisClient *redis.Client) ratelimit.Limiter {
	if redisClient == nil {
		return ratelimit.NewMemoryLimiter()
	}
	return ratelimit.NewRedisLimiter(redisClient)
}

func initPresenceStore(redisClient *redis.Client) presence.Store {
	if redisClient == nil {
		return presence.NewMemoryStore()
	}
	return presence.NewRedisStore(redisClient)
}

func startMultipartUploadCleanup(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			cleanupExpiredMultipartUploads(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func cleanupExpiredMultipartUploads(ctx context.Context) {
	cleaned, err := service.FileService.CleanupExpiredMultipartUploads(ctx, time.Now(), 100)
	if err != nil {
		logger.Warn("Failed to cleanup expired multipart uploads", logger.Any("error", err))
		return
	}
	if cleaned > 0 {
		logger.Info("Expired multipart uploads cleaned", logger.Any("count", cleaned))
	}
}

func buildServer(cfg *config.Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:           cfg.Server.Address(),
		Handler:        handler,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
}
