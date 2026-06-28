package storage

import (
	"context"
	"io"
)

// ObjectMeta 是保存对象时需要的文件元信息。
// OriginalName 只用于记录/展示，不能直接作为磁盘存储名。
type ObjectMeta struct {
	OriginalName string
	ContentType  string
	Size         int64
	Extension    string
}

// SaveObjectInput 是存储层保存文件所需的输入。
// Reader 提供文件内容流，Meta 提供文件名、大小、类型等辅助信息。
type SaveObjectInput struct {
	Reader io.Reader
	Meta   ObjectMeta
}

// StoredObject 描述文件保存后的存储结果。
// Key 是存储内部路径，URL 是按存储前缀生成的访问路径；SHA256 用于后续完整性校验。
// 最终是否允许公开访问由 service 判断。
type StoredObject struct {
	Key         string
	URL         string
	StoredName  string
	Size        int64
	ContentType string
	SHA256      string
}

// CompleteMultipartInput 是把临时分片合并成正式对象所需的参数。
type CompleteMultipartInput struct {
	UploadID    string
	TotalChunks int
	Meta        ObjectMeta
}

// Storage 抽象文件存储能力，当前实现是本地文件系统。
// 后续接入 OSS/MinIO/S3 时，只需要提供新的 Storage 实现。
type Storage interface {
	Save(ctx context.Context, input SaveObjectInput) (*StoredObject, error)
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	OpenRange(ctx context.Context, key string, start, end int64) (io.ReadCloser, error)
	SavePart(ctx context.Context, uploadID string, index int, reader io.Reader) (int64, error)
	CompleteMultipart(ctx context.Context, input CompleteMultipartInput) (*StoredObject, error)
	DeleteMultipart(ctx context.Context, uploadID string) error
	Delete(ctx context.Context, key string) error
	PublicURL(key string) string
}
