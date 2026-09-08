package repository

import (
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
