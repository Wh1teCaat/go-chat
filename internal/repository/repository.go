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

func (r *Repository) WithTransaction(fn func(tx *Repository) error) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		// 执行业务回调。
		return fn(&Repository{db: tx})
	})
}
