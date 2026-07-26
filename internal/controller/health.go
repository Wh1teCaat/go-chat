package controller

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// 健康检查探针由 main 注入，controller 不直接依赖数据库和 Redis 客户端。
var (
	healthDBPing    func(ctx context.Context) error
	healthRedisPing func(ctx context.Context) error
)

// InitHealthCheckers 注入健康检查探针。redisPing 传 nil 表示 Redis 未启用。
func InitHealthCheckers(dbPing, redisPing func(ctx context.Context) error) {
	healthDBPing = dbPing
	healthRedisPing = redisPing
}

// Health 是给负载均衡/容器编排用的健康检查端点：数据库不可用时返回 503，
// Redis 是可降级依赖，只如实上报状态，不影响整体健康判断。
func Health(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	status := http.StatusOK
	dbStatus := "ok"
	if healthDBPing == nil {
		dbStatus = "unknown"
	} else if err := healthDBPing(ctx); err != nil {
		dbStatus = "down"
		status = http.StatusServiceUnavailable
	}

	redisStatus := "disabled"
	if healthRedisPing != nil {
		redisStatus = "ok"
		if err := healthRedisPing(ctx); err != nil {
			redisStatus = "down"
		}
	}

	body := gin.H{"status": "ok", "db": dbStatus, "redis": redisStatus}
	if status != http.StatusOK {
		body["status"] = "unavailable"
	}
	c.JSON(status, body)
}
