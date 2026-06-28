package repository

import (
	"context"

	"chat_proj/internal/model"

	"gorm.io/gorm"
)

// FileRepository 定义文件元数据的持久化操作。
type FileRepository interface {
	CreateFile(ctx context.Context, file *model.File) error
	GetFileByID(ctx context.Context, id uint) (*model.File, error)
	GetFileByStorageKey(ctx context.Context, storageKey string) (*model.File, error)
	BindFileToConversation(ctx context.Context, fileID, uploaderID, conversationID uint) error
}

// CreateFile 创建一条文件元数据记录，不负责写入文件内容。
func (r *Repository) CreateFile(ctx context.Context, file *model.File) error {
	return r.db.WithContext(ctx).Create(file).Error
}

// GetFileByID 根据文件 ID 查询元数据，下载时用它拿到原文件名和 storage_key。
func (r *Repository) GetFileByID(ctx context.Context, id uint) (*model.File, error) {
	var file model.File
	if err := r.db.WithContext(ctx).First(&file, id).Error; err != nil {
		return nil, err
	}
	return &file, nil
}

// GetFileByStorageKey 根据内部存储 key 查询文件元数据。
// 公开 /uploads 访问会先查库确认用途，不能绕过业务权限直接读磁盘。
func (r *Repository) GetFileByStorageKey(ctx context.Context, storageKey string) (*model.File, error) {
	var file model.File
	if err := r.db.WithContext(ctx).Where("storage_key = ?", storageKey).First(&file).Error; err != nil {
		return nil, err
	}
	return &file, nil
}

// BindFileToConversation 把已上传文件标记为某个会话里的聊天文件。
// uploaderID 参与条件，避免用户通过伪造文件消息绑定别人的文件。
func (r *Repository) BindFileToConversation(ctx context.Context, fileID, uploaderID, conversationID uint) error {
	result := r.db.WithContext(ctx).
		Model(&model.File{}).
		Where("id = ? AND user_id = ?", fileID, uploaderID).
		Update("conversation_id", conversationID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
