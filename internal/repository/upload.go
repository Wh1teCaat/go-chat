package repository

import (
	"context"
	"sort"
	"time"

	"chat_proj/internal/model"

	"gorm.io/gorm/clause"
)

type UploadRepository interface {
	CreateUploadSession(ctx context.Context, session *model.UploadSession) error
	GetUploadSession(ctx context.Context, uploadID string) (*model.UploadSession, error)
	UpdateUploadSessionStatus(ctx context.Context, uploadID, status string) error
	ListExpiredUploadSessions(ctx context.Context, now time.Time, limit int) ([]model.UploadSession, error)
	CancelExpiredUploadSession(ctx context.Context, uploadID string, now time.Time) (bool, error)
	UpsertUploadChunk(ctx context.Context, chunk *model.UploadChunk) error
	ListUploadChunks(ctx context.Context, uploadID string) ([]model.UploadChunk, error)
	DeleteUploadChunks(ctx context.Context, uploadID string) error
}

// CreateUploadSession 创建一次分片上传会话，文件内容会先落到临时分片目录。
func (r *Repository) CreateUploadSession(ctx context.Context, session *model.UploadSession) error {
	return r.db.WithContext(ctx).Create(session).Error
}

// GetUploadSession 根据 upload_id 查询上传会话。
func (r *Repository) GetUploadSession(ctx context.Context, uploadID string) (*model.UploadSession, error) {
	var session model.UploadSession
	if err := r.db.WithContext(ctx).Where("upload_id = ?", uploadID).First(&session).Error; err != nil {
		return nil, err
	}
	return &session, nil
}

// UpdateUploadSessionStatus 更新上传会话状态，如 completed/canceled。
func (r *Repository) UpdateUploadSessionStatus(ctx context.Context, uploadID, status string) error {
	return r.db.WithContext(ctx).
		Model(&model.UploadSession{}).
		Where("upload_id = ?", uploadID).
		Update("status", status).Error
}

// ListExpiredUploadSessions 批量查询已经过期但尚未完成/取消的上传会话。
func (r *Repository) ListExpiredUploadSessions(ctx context.Context, now time.Time, limit int) ([]model.UploadSession, error) {
	if limit <= 0 {
		limit = 100
	}
	var sessions []model.UploadSession
	if err := r.db.WithContext(ctx).
		Where("status = ? AND expires_at < ?", model.UploadSessionStatusPending, now).
		Order("expires_at ASC").
		Limit(limit).
		Find(&sessions).Error; err != nil {
		return nil, err
	}
	return sessions, nil
}

// CancelExpiredUploadSession 只取消仍处于 pending 且确实过期的会话。
// RowsAffected 为 0 时表示它可能已被完成/取消，清理任务不应继续删除对应分片。
func (r *Repository) CancelExpiredUploadSession(ctx context.Context, uploadID string, now time.Time) (bool, error) {
	result := r.db.WithContext(ctx).
		Model(&model.UploadSession{}).
		Where("upload_id = ? AND status = ? AND expires_at < ?", uploadID, model.UploadSessionStatusPending, now).
		Update("status", model.UploadSessionStatusCanceled)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// UpsertUploadChunk 记录分片上传结果；重复上传同一 index 时覆盖 size，便于断点重传。
func (r *Repository) UpsertUploadChunk(ctx context.Context, chunk *model.UploadChunk) error {
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "upload_id"}, {Name: "index"}},
			DoUpdates: clause.AssignmentColumns([]string{"size", "updated_at"}),
		}).
		Create(chunk).Error
}

// ListUploadChunks 查询某次上传已经成功落盘的分片。
func (r *Repository) ListUploadChunks(ctx context.Context, uploadID string) ([]model.UploadChunk, error) {
	var chunks []model.UploadChunk
	if err := r.db.WithContext(ctx).Where("upload_id = ?", uploadID).Find(&chunks).Error; err != nil {
		return nil, err
	}
	sort.Slice(chunks, func(i, j int) bool {
		return chunks[i].Index < chunks[j].Index
	})
	return chunks, nil
}

// DeleteUploadChunks 删除某次上传的分片记录；磁盘分片由 storage.DeleteMultipart 负责清理。
func (r *Repository) DeleteUploadChunks(ctx context.Context, uploadID string) error {
	return r.db.WithContext(ctx).Where("upload_id = ?", uploadID).Delete(&model.UploadChunk{}).Error
}
