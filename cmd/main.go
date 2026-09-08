package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
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
	"chat_proj/internal/wsbus"
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

// run 按依赖配置启动应用，并负责各基础设施与 HTTP 服务的生命周期管理。
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
	// 后台清理随停机取消，避免关闭过程中还有 goroutine 在写数据库。
	cleanupCtx, stopCleanup := context.WithCancel(context.Background())
	defer stopCleanup()
	startMultipartUploadCleanup(cleanupCtx, time.Hour)

	redisClient, err := cache.NewRedisClient(context.Background(), cfg.Redis)
	if err != nil {
		logger.Error("Failed to initialize redis", logger.Any("error", err))
		deps.exit(1)
		return
	}
	logger.Info("Redis initialized successfully")
	redisStore := cache.NewRedisStore(redisClient)
	service.InitCacheStore(redisStore)
	service.InitTokenStore(redisStore)
	defer redisClient.Close()
	presenceStore := initPresenceStore(redisClient)
	service.InitPresenceStore(presenceStore)
	controller.InitPresenceStore(presenceStore)
	limiter := initRateLimiter(redisClient)
	controller.InitHealthCheckers(buildDBPing(db), buildRedisPing(redisClient))

	// 多实例部署时 WS 推送经 Redis Pub/Sub 广播；没有 Redis 就保持进程内直投（单实例）。
	var bus wsbus.Bus = wsbus.NewLocalBus(controller.WSHub)
	if redisClient != nil {
		redisBus, err := wsbus.NewRedisBus(context.Background(), redisClient, controller.WSHub)
		if err != nil {
			logger.Error("Failed to initialize ws bus", logger.Any("error", err))
			deps.exit(1)
			return
		}
		bus = redisBus
	}
	controller.InitWSBus(bus)

	server := buildServer(cfg, deps.newRouter(cfg, limiter))
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- deps.listenAndServe(server)
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("Server error", logger.Any("error", err))
			deps.exit(1)
		}
	case sig := <-quit:
		logger.Info("Shutdown signal received", logger.String("signal", sig.String()))
		stopCleanup()

		// 先停 HTTP 监听并等待存量请求结束；websocket 连接已脱离 net/http 管理，需要单独关闭。
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Warn("Server shutdown incomplete", logger.Any("error", err))
		}
		// 先退订总线再关本地连接，避免关闭过程中还往连接里投递跨实例消息。
		if err := bus.Close(); err != nil {
			logger.Warn("WS bus close failed", logger.Any("error", err))
		}
		controller.WSHub.CloseAll()
		logger.Info("Server stopped gracefully")
	}
}

// buildDBPing 构造用于健康检查的数据库连通性探测函数。
func buildDBPing(db *gorm.DB) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		sqlDB, err := db.DB()
		if err != nil {
			return err
		}
		return sqlDB.PingContext(ctx)
	}
}

// buildRedisPing 构造用于健康检查的 Redis 连通性探测函数。
func buildRedisPing(client *redis.Client) func(ctx context.Context) error {
	if client == nil {
		return nil
	}
	return func(ctx context.Context) error {
		return client.Ping(ctx).Err()
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

// startMultipartUploadCleanup 启动定时清理过期分片上传的后台任务。
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

// cleanupExpiredMultipartUploads 执行一批过期分片上传清理并记录结果。
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

// buildServer 根据配置和路由处理器创建 HTTP 服务。
func buildServer(cfg *config.Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:           cfg.Server.Address(),
		Handler:        handler,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
}
