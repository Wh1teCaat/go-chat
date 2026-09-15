package service

import (
	"context"
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
// jti 不在 allowlist 时可能是已登出、已轮换（重放）或服务端重启丢失，一律要求重新登录。
func (s *tokenService) RefreshTokenPair(ctx context.Context, refreshToken string) (*TokenPair, error) {
	claims, err := auth.ValidateToken(refreshToken, auth.TokenTypeRefresh)
	if err != nil {
		return nil, apperrors.WithCause(apperrors.ErrInvalidToken, "invalid refresh token", err)
	}
	if claims.ID == "" {
		// 旧版本签发的 refresh token 没有 jti，无法参与吊销体系，直接要求重新登录。
		return nil, apperrors.WithMessage(apperrors.ErrInvalidToken, "refresh token missing jti")
	}

	pair, newJTI, err := generateTokenPair(claims.UserID, claims.Username)
	if err != nil {
		return nil, err
	}
	user := strconv.FormatUint(uint64(claims.UserID), 10)
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
	if err := tokenStore.Delete(ctx, refreshTokenKey(claims.ID)); err != nil {
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
