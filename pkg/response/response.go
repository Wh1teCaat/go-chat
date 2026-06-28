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

func JSON(c *gin.Context, httpStatus, code int, message string, data interface{}) {
	c.JSON(httpStatus, Body{
		Code:      code,
		Message:   message,
		Data:      data,
		RequestID: c.GetString(RequestIDKey),
	})
}

func OK(c *gin.Context, data interface{}) {
	JSON(c, http.StatusOK, 0, "ok", data)
}

func Message(c *gin.Context, message string) {
	JSON(c, http.StatusOK, 0, message, nil)
}

func Error(c *gin.Context, err error) {
	status := apperrors.HTTPCode(err)
	logBusinessError(c, status, err)
	JSON(c, status, status, err.Error(), nil)
}

func BindError(c *gin.Context, err error) {
	logBindError(c, err)
	JSON(c, http.StatusBadRequest, http.StatusBadRequest, err.Error(), nil)
}

func Unauthorized(c *gin.Context, message string) {
	JSON(c, http.StatusUnauthorized, http.StatusUnauthorized, message, nil)
}

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
