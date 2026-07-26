package middleware

import (
	"chat_proj/internal/auth"
	"chat_proj/internal/ratelimit"
	"chat_proj/pkg/apperrors"
	"chat_proj/pkg/logger"
	"chat_proj/pkg/response"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

var publicRoutes = map[string]struct{}{
	http.MethodPost + " /v1/user/register": {},
	http.MethodPost + " /v1/user/login":    {},
	http.MethodPost + " /v1/user/refresh":  {},
	// logout 只依赖 body 里的 refresh token，access token 过期后也要能登出。
	http.MethodPost + " /v1/user/logout": {},
}

func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := strings.TrimSpace(c.GetHeader("X-Request-ID"))
		if requestID == "" {
			requestID = newRequestID()
		}

		c.Set(response.RequestIDKey, requestID)
		c.Header("X-Request-ID", requestID)
		c.Next()
	}
}

func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		logger.Info("HTTPRequest",
			logger.String("request_id", c.GetString(response.RequestIDKey)),
			logger.String("method", c.Request.Method),
			logger.String("path", c.Request.URL.Path),
			logger.String("client_ip", c.ClientIP()),
			logger.String("user_agent", c.Request.UserAgent()),
			logger.Any("status", c.Writer.Status()),
			logger.Any("latency_ms", time.Since(start).Milliseconds()),
		)
	}
}

func CORS(allowedOrigins []string) gin.HandlerFunc {
	originSet := map[string]struct{}{}
	for _, origin := range allowedOrigins {
		if origin = strings.TrimSpace(origin); origin != "" {
			originSet[origin] = struct{}{}
		}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if _, ok := originSet[origin]; ok {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
		}

		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization")
		c.Header("Access-Control-Expose-Headers", "Content-Length, X-Content-SHA256")
		c.Header("Access-Control-Max-Age", "86400")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

func RateLimit(limiter ratelimit.Limiter, limit int, window time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodOptions || limiter == nil || limit <= 0 || window <= 0 {
			c.Next()
			return
		}

		route := c.FullPath()
		if route == "" {
			route = c.Request.URL.Path
		}
		key := "rate_limit:" + c.ClientIP() + ":" + c.Request.Method + ":" + route
		allowed, err := limiter.Allow(c.Request.Context(), key, limit, window)
		if err != nil {
			// Redis 短暂故障时选择 fail-open，避免限流系统故障直接打断聊天主链路。
			logger.Warn("RateLimitAllowFailed",
				logger.String("request_id", c.GetString(response.RequestIDKey)),
				logger.String("path", c.Request.URL.Path),
				logger.String("error", err.Error()),
			)
			c.Next()
			return
		}
		if !allowed {
			c.Header("Retry-After", strconv.Itoa(int(window.Seconds())))
			response.JSON(c, http.StatusTooManyRequests, http.StatusTooManyRequests, "too many requests", nil)
			c.Abort()
			return
		}
		c.Next()
	}
}

func AuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodOptions {
			c.Next()
			return
		}

		if _, ok := publicRoutes[c.Request.Method+" "+c.FullPath()]; ok {
			c.Next()
			return
		}

		tokenString := bearerToken(c.GetHeader("Authorization"))
		if tokenString == "" {
			// 浏览器 WebSocket API 无法自定义 Authorization header，
			// 约定客户端把 token 放进 Sec-WebSocket-Protocol 的 "bearer.<token>" 条目。
			// 不再接受 query string 传 token，避免 token 进入访问日志和浏览器历史。
			tokenString = wsProtocolToken(c.GetHeader("Sec-WebSocket-Protocol"))
		}
		if tokenString == "" {
			response.Error(c, apperrors.ErrUnauthorized)
			c.Abort()
			return
		}

		claims, err := auth.ValidateToken(tokenString)
		if err != nil {
			response.Error(c, apperrors.WithCause(apperrors.ErrInvalidToken, "invalid token", err))
			c.Abort()
			return
		}

		c.Set("user_id", claims.UserID)
		c.Next()
	}
}

// wsProtocolToken 从 Sec-WebSocket-Protocol 头里解析 "bearer.<token>" 条目。
func wsProtocolToken(header string) string {
	for part := range strings.SplitSeq(header, ",") {
		part = strings.TrimSpace(part)
		if token, ok := strings.CutPrefix(part, "bearer."); ok {
			return token
		}
	}
	return ""
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}

func newRequestID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(bytes[:])
}
