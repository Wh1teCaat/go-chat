package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	defaultAccessTokenTTL            = 15 * time.Minute
	defaultRefreshTokenTTL           = 7 * 24 * time.Hour
	TokenTypeAccess        TokenType = "access"
	TokenTypeRefresh       TokenType = "refresh"
)

// TokenType 区分接口访问令牌和刷新令牌。
type TokenType string

type Claims struct {
	UserID    uint      `json:"user_id"`
	Username  string    `json:"username,omitempty"`
	TokenType TokenType `json:"token_type"`
	jwt.RegisteredClaims
}

var jwtSecret []byte

func Init(secret string) error {
	if strings.TrimSpace(secret) == "" {
		return errors.New("jwt secret cannot be empty")
	}
	jwtSecret = []byte(secret)
	return nil
}

// GenerateRefreshToken 签发 refresh token。返回的 jti 是这次签发的唯一 ID，
// 调用方要把它写进服务端 allowlist，刷新/登出时按 jti 轮换或吊销。
func GenerateRefreshToken(userID uint, username string) (token string, expireAt int64, jti string, err error) {
	jti, err = newTokenID()
	if err != nil {
		return "", 0, "", err
	}
	token, expireAt, err = buildToken(userID, username, TokenTypeRefresh, defaultRefreshTokenTTL, jti)
	return token, expireAt, jti, err
}

// GenerateAccessToken 为用户签发短期访问令牌并返回过期时间。
func GenerateAccessToken(userID uint, username string) (string, int64, error) {
	return buildToken(userID, username, TokenTypeAccess, defaultAccessTokenTTL, "")
}

// buildToken 使用指定类型、有效期和标识构造并签名 JWT。
func buildToken(userID uint, username string, tokenType TokenType, ttl time.Duration, jti string) (string, int64, error) {
	if len(jwtSecret) == 0 {
		return "", 0, errors.New("jwt secret not initialized")
	}

	claims := Claims{
		UserID:    userID,
		Username:  username,
		TokenType: tokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(jwtSecret)
	if err != nil {
		return "", 0, err
	}
	return tokenString, claims.ExpiresAt.Unix(), nil
}

func newTokenID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// ValidateToken 校验 JWT 的签名、有效期和指定用途，拒绝未知的预期类型。
func ValidateToken(tokenString string, expectedType TokenType) (*Claims, error) {
	if expectedType != TokenTypeAccess && expectedType != TokenTypeRefresh {
		return nil, errors.New("unsupported expected token type")
	}
	if len(jwtSecret) == 0 {
		return nil, errors.New("jwt secret not initialized")
	}

	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (any, error) {
		if token.Method == jwt.SigningMethodHS256 {
			return jwtSecret, nil
		}
		return nil, jwt.ErrTokenSignatureInvalid
	})
	if err != nil {
		switch {
		case errors.Is(err, jwt.ErrTokenExpired):
			return nil, fmt.Errorf("token has expired")
		case errors.Is(err, jwt.ErrTokenSignatureInvalid):
			return nil, fmt.Errorf("invalid token signature")
		case errors.Is(err, jwt.ErrTokenMalformed):
			return nil, fmt.Errorf("malformed token")
		default:
			return nil, fmt.Errorf("failed to parse token: %w", err)
		}
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		if claims.TokenType != expectedType {
			return nil, fmt.Errorf("invalid token type")
		}
		return claims, nil
	}
	return nil, fmt.Errorf("invalid token")
}
