// chat-logic 服务：拆分部署形态下的业务层。
// 承载全部 REST 接口和消息业务（校验/落库/推送编排），通过 gRPC 接受 gateway 转发的消息，
// 推送经 Redis Pub/Sub 总线广播给各 gateway 实例投递。不直接持有 WebSocket 连接。
package main

import (
	"context"
	"errors"
	"net"
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
	"chat_proj/internal/rpc/chatpb"
	"chat_proj/internal/service"
	"chat_proj/internal/storage"
	"chat_proj/internal/wsbus"
	"chat_proj/pkg/logger"

	"google.golang.org/grpc"
	"gorm.io/gorm"

	redislib "github.com/redis/go-redis/v9"
)

func main() {
	logger.InitLogger("logs/logic.log", "info")

	cfg, err := config.Load()
	if err != nil {
		logger.Error("Failed to load config", logger.Any("error", err))
		os.Exit(1)
	}
	logger.InitLogger(cfg.Log.Path, cfg.Log.Level)

	// 拆分部署必须有 Redis：没有总线，logic 产生的推送到不了 gateway 上的连接。
	if !cfg.Redis.Enabled {
		logger.Error("chat-logic requires redis (message bus); enable [redis] in config or use the monolith binary")
		os.Exit(1)
	}

	if err := auth.Init(cfg.JWT.Secret); err != nil {
		logger.Error("Failed to initialize auth", logger.Any("error", err))
		os.Exit(1)
	}

	db, err := database.InitDB(cfg.Database)
	if err != nil {
		logger.Error("Failed to initialize database", logger.Any("error", err))
		os.Exit(1)
	}

	service.Init(repository.NewRepository(db))
	service.InitFileStorage(storage.NewLocalStorage("uploads", "/uploads"))
	cleanupCtx, stopCleanup := context.WithCancel(context.Background())
	defer stopCleanup()
	startMultipartUploadCleanup(cleanupCtx, time.Hour)

	redisClient, err := cache.NewRedisClient(context.Background(), cfg.Redis)
	if err != nil || redisClient == nil {
		logger.Error("Failed to initialize redis", logger.Any("error", err))
		os.Exit(1)
	}
	defer redisClient.Close()
	service.InitCacheStore(cache.NewRedisStore(redisClient))
	service.InitTokenStore(cache.NewRedisStore(redisClient))
	presenceStore := presence.NewRedisStore(redisClient)
	service.InitPresenceStore(presenceStore)
	controller.InitHealthCheckers(buildDBPing(db), buildRedisPing(redisClient))

	// logic 只发布不订阅：它不持有连接，投递发生在 gateway。fallback 传 nil，
	// Redis 故障期间的推送靠客户端重连补拉兜底。
	bus := wsbus.NewRedisPublisher(redisClient, nil)
	controller.InitWSBus(bus)

	// gRPC 面向 gateway 暴露消息发送。
	grpcListener, err := net.Listen("tcp", cfg.GRPC.Addr)
	if err != nil {
		logger.Error("Failed to listen grpc", logger.Any("error", err))
		os.Exit(1)
	}
	grpcServer := grpc.NewServer()
	chatpb.RegisterChatServiceServer(grpcServer, controller.NewChatGRPCServer())
	go func() {
		if err := grpcServer.Serve(grpcListener); err != nil {
			logger.Error("gRPC server error", logger.Any("error", err))
		}
	}()
	logger.Info("gRPC server listening", logger.String("addr", cfg.GRPC.Addr))

	httpServer := &http.Server{
		Addr: cfg.Server.Address(),
		Handler: router.NewWithConfigAndOptions(cfg, router.Options{
			RateLimiter: ratelimit.NewRedisLimiter(redisClient),
			DisableWS:   true,
		}),
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- httpServer.ListenAndServe()
	}()
	logger.Info("chat-logic HTTP listening", logger.String("addr", cfg.Server.Address()))

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("Server error", logger.Any("error", err))
			os.Exit(1)
		}
	case sig := <-quit:
		logger.Info("Shutdown signal received", logger.String("signal", sig.String()))
		stopCleanup()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Warn("HTTP server shutdown incomplete", logger.Any("error", err))
		}
		// GracefulStop 等待进行中的 RPC 结束，保证 gateway 不会收到半途中断的响应。
		grpcServer.GracefulStop()
		logger.Info("chat-logic stopped gracefully")
	}
}

func buildDBPing(db *gorm.DB) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		sqlDB, err := db.DB()
		if err != nil {
			return err
		}
		return sqlDB.PingContext(ctx)
	}
}

func buildRedisPing(client *redislib.Client) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		return client.Ping(ctx).Err()
	}
}

func startMultipartUploadCleanup(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if cleaned, err := service.FileService.CleanupExpiredMultipartUploads(ctx, time.Now(), 100); err != nil {
				logger.Warn("Failed to cleanup expired multipart uploads", logger.Any("error", err))
			} else if cleaned > 0 {
				logger.Info("Expired multipart uploads cleaned", logger.Any("count", cleaned))
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
