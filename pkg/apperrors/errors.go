package apperrors

import (
	"errors"
	"net/http"
)

type appError struct {
	kind    error
	message string
	cause   error
}

func (e appError) Error() string {
	return e.message
}

func (e appError) Unwrap() error {
	return e.cause
}

func (e appError) Is(target error) bool {
	return errors.Is(e.kind, target)
}

// WithMessage 返回带安全文案的业务错误，同时保留标准错误类型供 errors.Is、HTTPCode 和 Code 使用。
func WithMessage(kind error, message string) error {
	return appError{kind: kind, message: message}
}

// WithCause 用于内部失败：客户端只看到安全文案，日志仍能通过 Cause 记录真实原因。
func WithCause(kind error, message string, cause error) error {
	return appError{kind: kind, message: message, cause: cause}
}

func Cause(err error) error {
	var appErr appError
	if errors.As(err, &appErr) {
		return appErr.cause
	}
	return nil
}

var (
	// ErrInvalidInput 映射 HTTP 400，表示请求参数或业务参数无效。
	ErrInvalidInput = errors.New("invalid input")

	// ErrEmptyFields 映射 HTTP 400，表示必填认证字段为空。
	ErrEmptyFields = errors.New("email and password cannot be empty")

	// ErrWrongPassword 映射 HTTP 401，表示认证字段存在但不正确。
	ErrWrongPassword = errors.New("wrong password")

	// ErrUnauthorized 映射 HTTP 401，表示缺少认证凭证。
	ErrUnauthorized = errors.New("missing token")

	// ErrInvalidToken 映射 HTTP 401，表示认证凭证存在但无效。
	ErrInvalidToken = errors.New("invalid token")

	// ErrPermissionDenied 映射 HTTP 403，表示当前用户无权访问资源。
	ErrPermissionDenied = errors.New("permission denied")

	// ErrUserNotFound 映射 HTTP 404，表示请求的用户不存在。
	ErrUserNotFound = errors.New("user not found")

	// ErrNotFound 映射 HTTP 404，表示非用户类资源不存在。
	ErrNotFound = errors.New("resource not found")

	// ErrEmailAlreadyExists 映射 HTTP 409，表示邮箱已注册。
	ErrEmailAlreadyExists = errors.New("email already registered")

	// ErrConflict 映射 HTTP 409，表示请求与当前资源状态冲突。
	ErrConflict = errors.New("resource conflict")

	// ErrDBOperation 映射 HTTP 500，表示数据库操作发生非预期失败。
	ErrDBOperation = errors.New("database operation failed")

	// ErrHashFailed 映射 HTTP 500，表示密码哈希发生非预期失败。
	ErrHashFailed = errors.New("password hashing failed")
)

// HTTPCode 根据业务错误类型返回对应的 HTTP 状态码
func HTTPCode(err error) int {
	switch {
	case errors.Is(err, ErrInvalidInput),
		errors.Is(err, ErrEmptyFields):
		return http.StatusBadRequest
	case errors.Is(err, ErrWrongPassword):
		return http.StatusUnauthorized
	case errors.Is(err, ErrUnauthorized),
		errors.Is(err, ErrInvalidToken):
		return http.StatusUnauthorized
	case errors.Is(err, ErrPermissionDenied):
		return http.StatusForbidden
	case errors.Is(err, ErrUserNotFound),
		errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrEmailAlreadyExists):
		return http.StatusConflict
	case errors.Is(err, ErrConflict):
		return http.StatusConflict
	case errors.Is(err, ErrDBOperation),
		errors.Is(err, ErrHashFailed):
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

func Code(err error) string {
	switch {
	case errors.Is(err, ErrInvalidInput):
		return "invalid_input"
	case errors.Is(err, ErrEmptyFields):
		return "empty_fields"
	case errors.Is(err, ErrWrongPassword):
		return "wrong_password"
	case errors.Is(err, ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, ErrInvalidToken):
		return "invalid_token"
	case errors.Is(err, ErrPermissionDenied):
		return "permission_denied"
	case errors.Is(err, ErrUserNotFound):
		return "user_not_found"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrEmailAlreadyExists):
		return "email_already_exists"
	case errors.Is(err, ErrConflict):
		return "conflict"
	case errors.Is(err, ErrDBOperation):
		return "db_operation_failed"
	case errors.Is(err, ErrHashFailed):
		return "hash_failed"
	default:
		return "internal_error"
	}
}
