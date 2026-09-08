package response

import (
	"net/http"

	"chat_proj/pkg/apperrors"
	"chat_proj/pkg/logger"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const RequestIDKey = "request_id"

type Body struct {
	Code      int         `json:"code"`
	Message   string      `json:"message"`
	Data      interface{} `json:"data,omitempty"`
	RequestID string      `json:"request_id,omitempty"`
}

// JSON 按统一响应结构向客户端写入 JSON 数据。
func JSON(c *gin.Context, httpStatus, code int, message string, data interface{}) {
	c.JSON(httpStatus, Body{
		Code:      code,
		Message:   message,
		Data:      data,
		RequestID: c.GetString(RequestIDKey),
	})
}

// OK 返回包含业务数据的成功响应。
func OK(c *gin.Context, data interface{}) {
	JSON(c, http.StatusOK, 0, "ok", data)
}

// Message 返回仅包含提示信息的成功响应。
func Message(c *gin.Context, message string) {
	JSON(c, http.StatusOK, 0, message, nil)
}

// Error 将业务错误映射为统一 HTTP 错误响应并记录日志。
func Error(c *gin.Context, err error) {
	status := apperrors.HTTPCode(err)
	logBusinessError(c, status, err)
	JSON(c, status, status, err.Error(), nil)
}

// BindError 返回请求参数绑定失败响应并记录具体原因。
func BindError(c *gin.Context, err error) {
	logBindError(c, err)
	JSON(c, http.StatusBadRequest, http.StatusBadRequest, err.Error(), nil)
}

// Unauthorized 返回未认证响应。
func Unauthorized(c *gin.Context, message string) {
	JSON(c, http.StatusUnauthorized, http.StatusUnauthorized, message, nil)
}

// logBusinessError 按状态码级别记录带请求上下文的业务错误。
func logBusinessError(c *gin.Context, status int, err error) {
	fields := []zap.Field{
		logger.String("request_id", c.GetString(RequestIDKey)),
		logger.String("code", apperrors.Code(err)),
		logger.Any("status", status),
		logger.String("method", c.Request.Method),
		logger.String("path", c.Request.URL.Path),
		logger.String("error", err.Error()),
	}
	if userID, ok := c.Get("user_id"); ok {
		fields = append(fields, logger.Any("user_id", userID))
	}
	if cause := apperrors.Cause(err); cause != nil {
		fields = append(fields, logger.String("cause", cause.Error()))
	}

	if status >= http.StatusInternalServerError {
		logger.Error("HTTPBusinessError", fields...)
		return
	}
	logger.Warn("HTTPBusinessError", fields...)
}

// logBindError 记录请求参数绑定错误及请求上下文。
func logBindError(c *gin.Context, err error) {
	fields := []zap.Field{
		logger.String("request_id", c.GetString(RequestIDKey)),
		logger.String("code", "bind_error"),
		logger.Any("status", http.StatusBadRequest),
		logger.String("method", c.Request.Method),
		logger.String("path", c.Request.URL.Path),
		logger.String("error", err.Error()),
	}
	if userID, ok := c.Get("user_id"); ok {
		fields = append(fields, logger.Any("user_id", userID))
	}
	logger.Warn("HTTPBindError", fields...)
}
