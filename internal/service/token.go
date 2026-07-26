package service

import (
	"context"
	"time"

	"chat_proj/internal/auth"
	"chat_proj/internal/cache"
	"chat_proj/pkg/apperrors"
)

type tokenService struct{}

var TokenService = new(tokenService)

// tokenStore 保存 refresh token 的 jti allowlist。
// 有 Redis 时用 Redis（多实例共享、重启不丢）；没有时退回内存实现，重启后所有会话需要重新登录。
var tokenStore cache.Store = cache.NewMemoryStore()

func InitTokenStore(store cache.Store) {
	if store == nil {
		store = cache.NewMemoryStore()
	}
	tokenStore = store
}

type TokenPair struct {
	AccessToken     string
	AccessExpireAt  int64
	RefreshToken    string
	RefreshExpireAt int64
}

type refreshTokenRecord struct {
	UserID uint `json:"user_id"`
}

func refreshTokenKey(jti string) string {
	return "auth:refresh:" + jti
}

// IssueTokenPair 签发 access + refresh token，并把 refresh token 的 jti 写入服务端 allowlist。
func (s *tokenService) IssueTokenPair(ctx context.Context, userID uint, username string) (*TokenPair, error) {
	accessToken, accessExpireAt, err := auth.GenerateAccessToken(userID, username)
	if err != nil {
		return nil, apperrors.WithCause(apperrors.ErrTokenOperation, "failed to generate token", err)
	}
	refreshToken, refreshExpireAt, jti, err := auth.GenerateRefreshToken(userID, username)
	if err != nil {
		return nil, apperrors.WithCause(apperrors.ErrTokenOperation, "failed to generate refresh token", err)
	}

	ttl := time.Until(time.Unix(refreshExpireAt, 0))
	if err := tokenStore.SetJSON(ctx, refreshTokenKey(jti), refreshTokenRecord{UserID: userID}, ttl); err != nil {
		return nil, apperrors.WithCause(apperrors.ErrTokenOperation, "failed to persist refresh token", err)
	}

	return &TokenPair{
		AccessToken:     accessToken,
		AccessExpireAt:  accessExpireAt,
		RefreshToken:    refreshToken,
		RefreshExpireAt: refreshExpireAt,
	}, nil
}

// RefreshTokenPair 校验并轮换 refresh token：旧 jti 立即吊销，返回新的 token 对。
// jti 不在 allowlist 时可能是已登出、已轮换（重放）或服务端重启丢失，一律要求重新登录。
func (s *tokenService) RefreshTokenPair(ctx context.Context, refreshToken string) (*TokenPair, error) {
	claims, err := auth.ValidateRefreshToken(refreshToken)
	if err != nil {
		return nil, apperrors.WithCause(apperrors.ErrInvalidToken, "invalid refresh token", err)
	}
	if claims.ID == "" {
		// 旧版本签发的 refresh token 没有 jti，无法参与吊销体系，直接要求重新登录。
		return nil, apperrors.WithMessage(apperrors.ErrInvalidToken, "refresh token missing jti")
	}

	var record refreshTokenRecord
	found, err := tokenStore.GetJSON(ctx, refreshTokenKey(claims.ID), &record)
	if err != nil {
		return nil, apperrors.WithCause(apperrors.ErrTokenOperation, "failed to load refresh token", err)
	}
	if !found || record.UserID != claims.UserID {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidToken, "refresh token revoked")
	}

	// 轮换：先吊销旧 jti 再签发新对，旧 token 从此不可再用，重放会在上面的 allowlist 检查中被拒绝。
	if err := tokenStore.Delete(ctx, refreshTokenKey(claims.ID)); err != nil {
		return nil, apperrors.WithCause(apperrors.ErrTokenOperation, "failed to rotate refresh token", err)
	}
	return s.IssueTokenPair(ctx, claims.UserID, claims.Username)
}

// RevokeRefreshToken 吊销 refresh token（登出）。token 无效或已吊销时视为已登出，不返回错误。
func (s *tokenService) RevokeRefreshToken(ctx context.Context, refreshToken string) error {
	claims, err := auth.ValidateRefreshToken(refreshToken)
	if err != nil || claims.ID == "" {
		return nil
	}
	if err := tokenStore.Delete(ctx, refreshTokenKey(claims.ID)); err != nil {
		return apperrors.WithCause(apperrors.ErrTokenOperation, "failed to revoke refresh token", err)
	}
	return nil
}
