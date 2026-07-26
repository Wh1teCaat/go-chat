// gateway 服务：拆分部署形态下的 WebSocket 接入层。
// 无业务状态，可按连接数水平扩容：JWT 认证后维持连接，来消息经 gRPC 转发给 chat-logic，
// 订阅 Redis Pub/Sub 总线把推送投递给本地在线用户。
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"chat_proj/internal/auth"
	"chat_proj/internal/cache"
	"chat_proj/internal/config"
	"chat_proj/internal/gateway"
	"chat_proj/internal/middleware"
	"chat_proj/internal/presence"
	"chat_proj/internal/rpc/chatpb"
	"chat_proj/internal/ws"
	"chat_proj/internal/wsbus"
	"chat_proj/pkg/logger"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	logger.InitLogger("logs/gateway.log", "info")

	cfg, err := config.Load()
	if err != nil {
		logger.Error("Failed to load config", logger.Any("error", err))
		os.Exit(1)
	}
	// gateway 与 logic 可能同机运行，日志分开：在配置目录下写 gateway-<原文件名>。
	logger.InitLogger(gatewayLogPath(cfg.Log.Path), cfg.Log.Level)

	if !cfg.Redis.Enabled {
		logger.Error("gateway requires redis (message bus + presence); enable [redis] in config")
		os.Exit(1)
	}
	if err := auth.Init(cfg.JWT.Secret); err != nil {
		logger.Error("Failed to initialize auth", logger.Any("error", err))
		os.Exit(1)
	}

	redisClient, err := cache.NewRedisClient(context.Background(), cfg.Redis)
	if err != nil || redisClient == nil {
		logger.Error("Failed to initialize redis", logger.Any("error", err))
		os.Exit(1)
	}
	defer redisClient.Close()

	hub := ws.NewHub()
	hub.SetPresenceStore(presence.NewRedisStore(redisClient))

	// 订阅总线：logic 发布的推送经这里投递给本实例的在线连接。
	// Kafka 模式下每个 gateway 实例用独立消费组（广播语义），组名带主机名保证唯一。
	var bus wsbus.Bus
	if cfg.Kafka.Enabled {
		if err := wsbus.EnsureTopic(context.Background(), cfg.Kafka.Brokers, cfg.Kafka.Topic, cfg.Kafka.Partitions); err != nil {
			logger.Error("Failed to ensure kafka topic", logger.Any("error", err))
			os.Exit(1)
		}
		// NewKafkaBus 会阻塞到消费组完成 join 且链路端到端可投递才返回，
		// 保证下面开始接受 WebSocket 连接时不存在"事件被 latest 位点跳过"的窗口。
		kafkaBus, err := wsbus.NewKafkaBus(context.Background(), cfg.Kafka.Brokers, cfg.Kafka.Topic, gatewayGroupID(), hub)
		if err != nil {
			logger.Error("Failed to initialize kafka bus", logger.Any("error", err))
			os.Exit(1)
		}
		bus = kafkaBus
		logger.Info("Kafka event bus enabled", logger.String("topic", cfg.Kafka.Topic))
	} else {
		redisBus, err := wsbus.NewRedisBus(context.Background(), redisClient, hub)
		if err != nil {
			logger.Error("Failed to initialize ws bus", logger.Any("error", err))
			os.Exit(1)
		}
		bus = redisBus
	}

	// gRPC 连接 chat-logic。内网服务间调用，暂用明文；上生产应换 mTLS。
	// 用 passthrough 绕开 grpc-go 自带的 DNS resolver：它会串行发 TXT（service config）
	// 查询，Docker 内置 DNS 常不响应这类查询，首次 RPC 会白等 5s+ 超时。
	// passthrough 直接用系统解析器建连，单个 logic 地址不需要客户端负载均衡。
	target := cfg.Gateway.LogicAddr
	if !strings.Contains(target, "://") {
		target = "passthrough:///" + target
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		logger.Error("Failed to dial chat-logic", logger.Any("error", err))
		os.Exit(1)
	}
	defer conn.Close()
	// 启动即预热连接，首条用户消息不吃建连延迟。
	conn.Connect()

	gw := gateway.New(hub, chatpb.NewChatServiceClient(conn), cfg.CORS.AllowedOrigins)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.RequestID())
	r.Use(middleware.RequestLogger())
	r.Use(middleware.CORS(cfg.CORS.AllowedOrigins))
	r.GET("/health", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := redisClient.Ping(ctx).Err(); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "redis": "down"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "redis": "ok"})
	})
	r.Use(middleware.AuthRequired())
	r.GET("/v1/ws", gw.ConnectWS)

	server := &http.Server{
		Addr:           cfg.Gateway.Address(),
		Handler:        r,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.ListenAndServe()
	}()
	logger.Info("gateway listening", logger.String("addr", cfg.Gateway.Address()),
		logger.String("logic_addr", cfg.Gateway.LogicAddr))

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
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Warn("Server shutdown incomplete", logger.Any("error", err))
		}
		// 顺序同单体：退订总线 → 关闭本地连接。客户端会自动重连到其他 gateway 实例。
		if err := bus.Close(); err != nil {
			logger.Warn("WS bus close failed", logger.Any("error", err))
		}
		hub.CloseAll()
		logger.Info("gateway stopped gracefully")
	}
}

// gatewayGroupID 生成本次启动的 Kafka 消费组名，必须做到"每次启动全局唯一"：
//   - 每实例独立组 = 广播语义（组内是分摊，同组的两个 gateway 会各丢一半事件）；
//   - 每次启动新组 = latest 位点确定生效，重启不会重放宕机期积压
//     （好友/群通知没有去重键，重放会产生可见的重复通知）；
//   - 只用主机名会在裸机同机多进程时碰撞，必须叠加进程内随机后缀。
//
// 空闲旧组的元数据由 broker 按 offsets.retention.minutes 自动清理。
func gatewayGroupID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("chat-gateway-%s-%d", host, os.Getpid())
	}
	return fmt.Sprintf("chat-gateway-%s-%s", host, hex.EncodeToString(buf[:]))
}

func gatewayLogPath(base string) string {
	dir, file := filepath.Split(base)
	if file == "" {
		file = "app.log"
	}
	// 带上主机名（容器内是 container id）：--scale gateway=N 时多个副本共享日志卷，
	// 写同一个文件会破坏 lumberjack 的轮转。
	if host, err := os.Hostname(); err == nil && host != "" {
		return filepath.Join(dir, "gateway-"+host+"-"+file)
	}
	return filepath.Join(dir, "gateway-"+file)
}
