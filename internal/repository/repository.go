package repository

import (
	"context"

	"gorm.io/gorm"
)

type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// WithTransaction 在数据库事务中执行回调，并根据返回错误提交或回滚。
func (r *Repository) WithTransaction(fn func(tx *Repository) error) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		// 执行业务回调。
		return fn(&Repository{db: tx})
	})
}

// EnableAsyncCommitForTransaction 只对当前事务关闭同步 WAL 等待。它适合允许在主机
// 断电时丢失极少量已确认消息的低延迟部署；连接池的其它事务不受影响。
func (r *Repository) EnableAsyncCommitForTransaction(ctx context.Context) error {
	return r.db.WithContext(ctx).Exec("SET LOCAL synchronous_commit = 'off'").Error
}
