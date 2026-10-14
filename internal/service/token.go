package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"chat_proj/internal/auth"
	"chat_proj/internal/cache"
	"chat_proj/pkg/apperrors"
)

type tokenService struct{}

var TokenService = new(tokenService)

// tokenStore 保存 refresh token 的 jti allowlist。
// Redis 保存有效刷新令牌；服务端必须注入已连接的存储。
var tokenStore cache.TokenStore = cache.NewRedisStore(nil)

func InitTokenStore(store cache.TokenStore) {
	if store == nil {
		store = cache.NewRedisStore(nil)
	}
	tokenStore = store
}

type TokenPair struct {
	AccessToken     string
	AccessExpireAt  int64
	RefreshToken    string
	RefreshExpireAt int64
}

// refreshTokenKey 返回刷新令牌标识对应的缓存键。
func refreshTokenKey(jti string) string {
	return "v2:auth:refresh:" + jti
}

// refreshResultKey 保存短期的加密刷新结果。
func refreshResultKey(jti string) string { return "v2:auth:refresh_result:" + jti }

// generateTokenPair 生成令牌及随机标识，不修改存储。
func generateTokenPair(userID uint, username string) (*TokenPair, string, error) {
	accessToken, accessExpireAt, err := auth.GenerateAccessToken(userID, username)
	if err != nil {
		return nil, "", apperrors.WithCause(apperrors.ErrTokenOperation, "failed to generate token", err)
	}
	refreshToken, refreshExpireAt, jti, err := auth.GenerateRefreshToken(userID, username)
	if err != nil {
		return nil, "", apperrors.WithCause(apperrors.ErrTokenOperation, "failed to generate refresh token", err)
	}

	return &TokenPair{
		AccessToken:     accessToken,
		AccessExpireAt:  accessExpireAt,
		RefreshToken:    refreshToken,
		RefreshExpireAt: refreshExpireAt,
	}, jti, nil
}

// RefreshTokenPair 校验并轮换 refresh token：旧 jti 立即吊销，返回新的 token 对。
// 同一 requestID 可在 60 秒内取回首次结果；其它重放或状态丢失要求重新登录。
func (s *tokenService) RefreshTokenPair(ctx context.Context, refreshToken, requestID string) (*TokenPair, error) {
	if requestID != "" {
		if _, err := hex.DecodeString(requestID); err != nil || len(requestID) != 32 {
			return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "requestID must be 32 hexadecimal characters")
		}
	}

	claims, err := auth.ValidateToken(refreshToken, auth.TokenTypeRefresh)
	if err != nil {
		return nil, apperrors.WithCause(apperrors.ErrInvalidToken, "invalid refresh token", err)
	}
	if claims.ID == "" || claims.ExpiresAt == nil {
		// 无标识或过期时间的令牌不能参与吊销和有界恢复。
		return nil, apperrors.WithMessage(apperrors.ErrInvalidToken, "refresh token missing jti or expiry")
	}

	pair, newJTI, err := generateTokenPair(claims.UserID, claims.Username)
	if err != nil {
		return nil, err
	}
	user := strconv.FormatUint(uint64(claims.UserID), 10)
	if requestID != "" {
		encoded, err := json.Marshal(pair)
		if err != nil {
			return nil, err
		}
		binding := claims.ID + ":" + requestID + ":" + user
		sealed, err := auth.SealRefreshResult(encoded, binding)
		if err != nil {
			return nil, apperrors.WithCause(apperrors.ErrTokenOperation, "failed to encrypt refresh result", err)
		}
		resultTTL := time.Until(claims.ExpiresAt.Time)
		if resultTTL > time.Minute {
			resultTTL = time.Minute
		}
		result, err := tokenStore.RotateToken(ctx, cache.TokenRotation{
			OldKey: refreshTokenKey(claims.ID), NewKey: refreshTokenKey(newJTI), ResultKey: refreshResultKey(claims.ID),
			UserID: user, RequestID: requestID, EncryptedResult: sealed,
			TokenTTL: time.Until(time.Unix(pair.RefreshExpireAt, 0)), ResultTTL: resultTTL,
		})
		if err != nil {
			return nil, apperrors.WithCause(apperrors.ErrTokenOperation, "failed to rotate refresh token", err)
		}
		if result == nil {
			return nil, apperrors.WithMessage(apperrors.ErrInvalidToken, "refresh token revoked")
		}
		plaintext, err := auth.OpenRefreshResult(result.EncryptedResult, binding)
		if err == nil {
			err = json.Unmarshal(plaintext, pair)
		}
		if err == nil {
			var refreshed *auth.Claims
			refreshed, err = auth.ValidateToken(pair.RefreshToken, auth.TokenTypeRefresh)
			if err == nil && (refreshed.UserID != claims.UserID || refreshTokenKey(refreshed.ID) != result.NewKey) {
				err = errors.New("cached refresh result binding mismatch")
			}
		}
		if err != nil {
			return nil, apperrors.WithCause(apperrors.ErrTokenOperation, "invalid cached refresh result", err)
		}
		return pair, nil
	}
	rotated, err := tokenStore.RotateString(ctx, refreshTokenKey(claims.ID), refreshTokenKey(newJTI), user, user, time.Until(time.Unix(pair.RefreshExpireAt, 0)))
	if err != nil {
		return nil, apperrors.WithCause(apperrors.ErrTokenOperation, "failed to rotate refresh token", err)
	}
	if !rotated {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidToken, "refresh token revoked")
	}
	return pair, nil
}

// RevokeRefreshToken 吊销 refresh token（登出）。token 无效或已吊销时视为已登出，不返回错误。
func (s *tokenService) RevokeRefreshToken(ctx context.Context, refreshToken string) error {
	claims, err := auth.ValidateToken(refreshToken, auth.TokenTypeRefresh)
	if err != nil || claims.ID == "" {
		return nil
	}
	if err := tokenStore.RevokeToken(ctx, refreshTokenKey(claims.ID), refreshResultKey(claims.ID), strconv.FormatUint(uint64(claims.UserID), 10)); err != nil {
		return apperrors.WithCause(apperrors.ErrTokenOperation, "failed to revoke refresh token", err)
	}
	return nil
}

// IssueTokenPair 签发令牌并保存用户 ID 字符串，有效期从本次签发起计算。
func (s *tokenService) IssueTokenPair(ctx context.Context, userID uint, username string) (*TokenPair, error) {
	pair, jti, err := generateTokenPair(userID, username)
	if err != nil {
		return nil, err
	}
	if err := tokenStore.SetString(ctx, refreshTokenKey(jti), strconv.FormatUint(uint64(userID), 10), time.Until(time.Unix(pair.RefreshExpireAt, 0))); err != nil {
		return nil, apperrors.WithCause(apperrors.ErrTokenOperation, "failed to persist refresh token", err)
	}
	return pair, nil
}
