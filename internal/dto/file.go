package dto

// FilePurpose 表示上传文件的业务用途，用于服务端选择校验规则。
type FilePurpose string

const (
	FilePurposeAvatar    FilePurpose = "avatar"
	FilePurposeChatImage FilePurpose = "chat_image"
	FilePurposeChatFile  FilePurpose = "chat_file"
)

// UploadFileOutput 是文件上传成功后返回给前端的文件信息。
// Filename 保留用户原始文件名用于展示，URL 指向后端生成的安全访问地址。
type UploadFileOutput struct {
	ID          uint        `json:"id,omitempty"`
	URL         string      `json:"url"`
	Filename    string      `json:"filename"`
	Size        int64       `json:"size"`
	ContentType string      `json:"contentType"`
	SHA256      string      `json:"sha256,omitempty"`
	Purpose     FilePurpose `json:"purpose,omitempty"`
}

type InitMultipartUploadInput struct {
	Filename    string      `json:"filename" binding:"required"`
	Size        int64       `json:"size" binding:"required"`
	ContentType string      `json:"contentType"`
	Purpose     FilePurpose `json:"purpose"`
	ChunkSize   int64       `json:"chunkSize"`
	SHA256      string      `json:"sha256"`
}

type MultipartUploadOutput struct {
	UploadID       string `json:"uploadID"`
	ChunkSize      int64  `json:"chunkSize"`
	TotalChunks    int    `json:"totalChunks"`
	UploadedChunks []int  `json:"uploadedChunks"`
	Status         string `json:"status,omitempty"`
}
