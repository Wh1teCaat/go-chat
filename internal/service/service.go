package service

import (
	"chat_proj/internal/cache"
	"chat_proj/internal/repository"
	"chat_proj/pkg/apperrors"
)

var repo *repository.Repository
var cacheStore cache.Store = cache.NewNoopStore()

func Init(r *repository.Repository) {
	repo = r
}

func InitCacheStore(store cache.Store) {
	if store == nil {
		store = cache.NewNoopStore()
	}
	cacheStore = store
}

// dbOperationError 把仓库层细节保存在日志 cause 中，同时给 HTTP/WS 返回稳定的安全文案。
func dbOperationError(err error) error {
	return apperrors.WithCause(apperrors.ErrDBOperation, "database operation failed", err)
}
