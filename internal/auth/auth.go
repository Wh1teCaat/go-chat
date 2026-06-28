package auth

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	defaultAccessTokenTTL  = 15 * time.Minute
	defaultRefreshTokenTTL = 7 * 24 * time.Hour
	tokenTypeAccess        = "access"
	tokenTypeRefresh       = "refresh"
)

type Claims struct {
	UserID    uint   `json:"user_id"`
	Username  string `json:"username,omitempty"`
	TokenType string `json:"token_type"`
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

func GenerateRefreshToken(userID uint, username string) (string, int64, error) {
	return buildToken(userID, username, tokenTypeRefresh, defaultRefreshTokenTTL)
}

func GenerateAccessToken(userID uint, username string) (string, int64, error) {
	return buildToken(userID, username, tokenTypeAccess, defaultAccessTokenTTL)
}

func buildToken(userID uint, username, tokenType string, ttl time.Duration) (string, int64, error) {
	if len(jwtSecret) == 0 {
		return "", 0, errors.New("jwt secret not initialized")
	}

	claims := Claims{
		UserID:    userID,
		Username:  username,
		TokenType: tokenType,
		RegisteredClaims: jwt.RegisteredClaims{
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

func ValidateToken(tokenString string) (*Claims, error) {
	return validateToken(tokenString, tokenTypeAccess)
}

func ValidateRefreshToken(tokenString string) (*Claims, error) {
	return validateToken(tokenString, tokenTypeRefresh)
}

func validateToken(tokenString, expectedType string) (*Claims, error) {
	if len(jwtSecret) == 0 {
		return nil, errors.New("jwt secret not initialized")
	}

	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
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
