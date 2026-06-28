package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"mime/multipart"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"chat_proj/internal/dto"
	"chat_proj/internal/model"
	"chat_proj/internal/storage"
	"chat_proj/pkg/apperrors"

	"gorm.io/gorm"
)

// maxUploadSize 限制单个上传文件大小，当前为 20MB。
const maxUploadSize = 20 << 20
const maxMultipartUploadSize = 500 << 20
const defaultMultipartChunkSize = 2 << 20
const defaultMultipartCleanupBatchSize = 100

const multipartSessionTTL = 24 * time.Hour

type fileService struct {
	store storage.Storage
}

var FileService = new(fileService)

// UploadFileInput 是 controller 调用文件服务时的内部输入。
// File/Header 来自 multipart 请求，UploaderID 来自鉴权结果，不由客户端直接传入。
type UploadFileInput struct {
	File         multipart.File
	Header       *multipart.FileHeader
	Purpose      dto.FilePurpose
	UploaderID   uint
	ContentType  string
	ClientSHA256 string
}

// DownloadFileResult 是 service 返回给 controller 的下载内容和响应元信息。
type DownloadFileResult struct {
	OriginalName string
	ContentType  string
	Size         int64
	SHA256       string
	Content      io.ReadCloser
}

type DownloadByteRange struct {
	Start int64
	End   int64
}

type InitMultipartUploadInput struct {
	Filename    string
	Size        int64
	ContentType string
	Purpose     dto.FilePurpose
	ChunkSize   int64
	UploaderID  uint
	SHA256      string
}

type UploadMultipartChunkInput struct {
	UploadID   string
	Index      int
	Reader     io.Reader
	Size       int64
	UploaderID uint
}

// InitFileStorage 注入文件存储实现，当前启动时注入 LocalStorage。
func InitFileStorage(store storage.Storage) {
	FileService.store = store
}

func (s *fileService) InitMultipartUpload(ctx context.Context, input InitMultipartUploadInput) (*dto.MultipartUploadOutput, error) {
	if s.store == nil {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "file storage is not initialized")
	}
	if input.Size <= 0 {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "file is empty")
	}
	if input.Size > maxMultipartUploadSize {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "file is too large")
	}

	purpose := normalizeFilePurpose(input.Purpose)
	if !isValidFilePurpose(purpose) {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "invalid file purpose")
	}
	originalName := cleanOriginalFilename(input.Filename)
	ext := strings.ToLower(filepath.Ext(originalName))
	contentType := normalizeMultipartContentType(input.ContentType)
	if !isAllowedUpload(purpose, ext, contentType) {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "file type is not allowed")
	}
	expectedSHA256, err := normalizeClientSHA256(input.SHA256)
	if err != nil {
		return nil, err
	}

	chunkSize := input.ChunkSize
	if chunkSize <= 0 {
		chunkSize = defaultMultipartChunkSize
	}
	if chunkSize <= 0 || chunkSize > defaultMultipartChunkSize*4 {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "invalid chunk size")
	}

	uploadID, err := randomUploadID()
	if err != nil {
		return nil, apperrors.WithCause(apperrors.ErrDBOperation, "failed to generate upload id", err)
	}
	totalChunks := int((input.Size + chunkSize - 1) / chunkSize)
	session := &model.UploadSession{
		UploadID:     uploadID,
		UserID:       input.UploaderID,
		OriginalName: originalName,
		ContentType:  contentType,
		Size:         input.Size,
		Purpose:      string(purpose),
		ChunkSize:    chunkSize,
		TotalChunks:  totalChunks,
		SHA256:       expectedSHA256,
		Status:       model.UploadSessionStatusPending,
		ExpiresAt:    time.Now().Add(multipartSessionTTL),
	}
	if err := repo.CreateUploadSession(ctx, session); err != nil {
		return nil, dbOperationError(err)
	}

	return &dto.MultipartUploadOutput{
		UploadID:       uploadID,
		ChunkSize:      chunkSize,
		TotalChunks:    totalChunks,
		UploadedChunks: []int{},
		Status:         model.UploadSessionStatusPending,
	}, nil
}

func (s *fileService) UploadMultipartChunk(ctx context.Context, input UploadMultipartChunkInput) error {
	if input.Reader == nil {
		return apperrors.WithMessage(apperrors.ErrInvalidInput, "chunk is required")
	}
	session, err := s.getWritableUploadSession(ctx, input.UploaderID, input.UploadID)
	if err != nil {
		return err
	}
	if input.Index < 0 || input.Index >= session.TotalChunks {
		return apperrors.WithMessage(apperrors.ErrInvalidInput, "invalid chunk index")
	}
	expectedSize := expectedChunkSize(session, input.Index)
	if input.Size != expectedSize {
		return apperrors.WithMessage(apperrors.ErrInvalidInput, "invalid chunk size")
	}

	written, err := s.store.SavePart(ctx, session.UploadID, input.Index, input.Reader)
	if err != nil {
		return err
	}
	if written != expectedSize {
		return apperrors.WithMessage(apperrors.ErrInvalidInput, "invalid chunk content size")
	}
	return repo.UpsertUploadChunk(ctx, &model.UploadChunk{
		UploadID: session.UploadID,
		Index:    input.Index,
		Size:     written,
	})
}

func (s *fileService) GetMultipartUploadStatus(ctx context.Context, userID uint, uploadID string) (*dto.MultipartUploadOutput, error) {
	session, err := s.getUploadSession(ctx, userID, uploadID)
	if err != nil {
		return nil, err
	}
	chunks, err := repo.ListUploadChunks(ctx, uploadID)
	if err != nil {
		return nil, dbOperationError(err)
	}
	return &dto.MultipartUploadOutput{
		UploadID:       session.UploadID,
		ChunkSize:      session.ChunkSize,
		TotalChunks:    session.TotalChunks,
		UploadedChunks: chunkIndexes(chunks),
		Status:         session.Status,
	}, nil
}

func (s *fileService) CompleteMultipartUpload(ctx context.Context, userID uint, uploadID string) (*dto.UploadFileOutput, error) {
	session, err := s.getWritableUploadSession(ctx, userID, uploadID)
	if err != nil {
		return nil, err
	}
	chunks, err := repo.ListUploadChunks(ctx, uploadID)
	if err != nil {
		return nil, dbOperationError(err)
	}
	if err := ensureAllChunksPresent(session, chunks); err != nil {
		return nil, err
	}

	stored, err := s.store.CompleteMultipart(ctx, storage.CompleteMultipartInput{
		UploadID:    session.UploadID,
		TotalChunks: session.TotalChunks,
		Meta: storage.ObjectMeta{
			OriginalName: session.OriginalName,
			ContentType:  session.ContentType,
			Size:         session.Size,
			Extension:    filepath.Ext(session.OriginalName),
		},
	})
	if err != nil {
		return nil, err
	}
	if stored.Size != session.Size {
		_ = s.store.Delete(ctx, stored.Key)
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "completed file size mismatch")
	}
	if err := s.ensureStoredSHA256(ctx, stored, session.SHA256); err != nil {
		return nil, err
	}

	file := &model.File{
		UserID:       session.UserID,
		OriginalName: session.OriginalName,
		StoredName:   stored.StoredName,
		StorageKey:   stored.Key,
		URL:          stored.URL,
		ContentType:  session.ContentType,
		Size:         stored.Size,
		SHA256:       stored.SHA256,
		Purpose:      session.Purpose,
	}
	if err := repo.CreateFile(ctx, file); err != nil {
		_ = s.store.Delete(ctx, stored.Key)
		return nil, dbOperationError(err)
	}
	if err := repo.UpdateUploadSessionStatus(ctx, uploadID, model.UploadSessionStatusCompleted); err != nil {
		return nil, dbOperationError(err)
	}
	_ = s.store.DeleteMultipart(ctx, uploadID)

	return &dto.UploadFileOutput{
		ID:          file.ID,
		URL:         uploadResponseURL(file),
		Filename:    file.OriginalName,
		Size:        file.Size,
		ContentType: file.ContentType,
		SHA256:      file.SHA256,
		Purpose:     dto.FilePurpose(file.Purpose),
	}, nil
}

func (s *fileService) CancelMultipartUpload(ctx context.Context, userID uint, uploadID string) error {
	session, err := s.getUploadSession(ctx, userID, uploadID)
	if err != nil {
		return err
	}
	if session.Status == model.UploadSessionStatusCompleted {
		return apperrors.WithMessage(apperrors.ErrConflict, "upload already completed")
	}
	if err := repo.UpdateUploadSessionStatus(ctx, uploadID, model.UploadSessionStatusCanceled); err != nil {
		return dbOperationError(err)
	}
	return s.store.DeleteMultipart(ctx, uploadID)
}

// CleanupExpiredMultipartUploads 清理过期未完成的分片上传会话。
// 清理顺序是先把 pending 会话原子标记为 canceled，再删除磁盘分片和分片记录，避免并发完成时误删文件。
func (s *fileService) CleanupExpiredMultipartUploads(ctx context.Context, now time.Time, limit int) (int, error) {
	if s.store == nil {
		return 0, apperrors.WithMessage(apperrors.ErrInvalidInput, "file storage is not initialized")
	}
	if limit <= 0 {
		limit = defaultMultipartCleanupBatchSize
	}

	sessions, err := repo.ListExpiredUploadSessions(ctx, now, limit)
	if err != nil {
		return 0, dbOperationError(err)
	}

	cleaned := 0
	for _, session := range sessions {
		canceled, err := repo.CancelExpiredUploadSession(ctx, session.UploadID, now)
		if err != nil {
			return cleaned, dbOperationError(err)
		}
		if !canceled {
			continue
		}
		if err := s.store.DeleteMultipart(ctx, session.UploadID); err != nil {
			return cleaned, err
		}
		if err := repo.DeleteUploadChunks(ctx, session.UploadID); err != nil {
			return cleaned, dbOperationError(err)
		}
		cleaned++
	}
	return cleaned, nil
}

// UploadFile 校验上传参数，保存文件内容，并写入 files 元数据表。
// 文件落盘使用随机名，返回给前端的 Filename 仍然是用户原始文件名。
func (s *fileService) UploadFile(ctx context.Context, input UploadFileInput) (*dto.UploadFileOutput, error) {
	if input.File == nil || input.Header == nil {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "file is required")
	}
	if s.store == nil {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "file storage is not initialized")
	}
	if input.Header.Size <= 0 {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "file is empty")
	}
	if input.Header.Size > maxUploadSize {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "file is too large")
	}
	purpose := normalizeFilePurpose(input.Purpose)
	if !isValidFilePurpose(purpose) {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "invalid file purpose")
	}

	originalName := cleanOriginalFilename(input.Header.Filename)
	ext := strings.ToLower(filepath.Ext(originalName))
	contentType := normalizeContentType(input.ContentType, input.Header)
	if !isAllowedUpload(purpose, ext, contentType) {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "file type is not allowed")
	}
	expectedSHA256, err := normalizeClientSHA256(input.ClientSHA256)
	if err != nil {
		return nil, err
	}

	stored, err := s.store.Save(ctx, storage.SaveObjectInput{
		Reader: input.File,
		Meta: storage.ObjectMeta{
			OriginalName: originalName,
			ContentType:  contentType,
			Size:         input.Header.Size,
			Extension:    ext,
		},
	})
	if err != nil {
		return nil, err
	}
	if err := s.ensureStoredSHA256(ctx, stored, expectedSHA256); err != nil {
		return nil, err
	}

	// 数据库保存原文件名和随机存储名的映射，用户展示用原名，磁盘访问用 URL/key。
	file := &model.File{
		UserID:       input.UploaderID,
		OriginalName: originalName,
		StoredName:   stored.StoredName,
		StorageKey:   stored.Key,
		URL:          stored.URL,
		ContentType:  contentType,
		Size:         stored.Size,
		SHA256:       stored.SHA256,
		Purpose:      string(purpose),
	}
	if err := repo.CreateFile(ctx, file); err != nil {
		// 文件已经落盘但元数据写库失败时回滚本地文件，避免产生无人引用的对象。
		_ = s.store.Delete(ctx, stored.Key)
		return nil, dbOperationError(err)
	}

	return &dto.UploadFileOutput{
		ID:          file.ID,
		URL:         uploadResponseURL(file),
		Filename:    file.OriginalName,
		Size:        file.Size,
		ContentType: file.ContentType,
		SHA256:      file.SHA256,
		Purpose:     purpose,
	}, nil
}

// GetPublicFile 只允许公开读取头像文件。
// 聊天附件即使知道 storage_key，也必须走带鉴权的下载接口，避免 /uploads 绕过会话权限。
func (s *fileService) GetPublicFile(ctx context.Context, storageKey string) (*DownloadFileResult, error) {
	if s.store == nil {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "file storage is not initialized")
	}

	storageKey = cleanStorageKey(storageKey)
	if storageKey == "" {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "invalid storage key")
	}

	file, err := repo.GetFileByStorageKey(ctx, storageKey)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.WithMessage(apperrors.ErrNotFound, "file not found")
		}
		return nil, dbOperationError(err)
	}

	if dto.FilePurpose(file.Purpose) != dto.FilePurposeAvatar {
		return nil, apperrors.WithMessage(apperrors.ErrNotFound, "file not found")
	}

	content, err := s.store.Open(ctx, file.StorageKey)
	if err != nil {
		return nil, err
	}
	contentType := strings.TrimSpace(file.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return &DownloadFileResult{
		OriginalName: cleanOriginalFilename(file.OriginalName),
		ContentType:  contentType,
		Size:         file.Size,
		SHA256:       file.SHA256,
		Content:      content,
	}, nil
}

// GetDownloadFile 根据文件 ID 查询元数据、校验下载权限，并打开真实文件内容。
// 上传者可下载；被发送到会话里的文件，当前会话成员也可下载。
func (s *fileService) GetDownloadFile(ctx context.Context, requesterID, fileID uint) (*DownloadFileResult, error) {
	return s.getDownloadFile(ctx, requesterID, fileID, nil)
}

// GetDownloadFileRange 打开文件的指定字节区间，用于 HTTP Range 断点下载。
func (s *fileService) GetDownloadFileRange(ctx context.Context, requesterID, fileID uint, byteRange DownloadByteRange) (*DownloadFileResult, error) {
	return s.getDownloadFile(ctx, requesterID, fileID, &byteRange)
}

func (s *fileService) getDownloadFile(ctx context.Context, requesterID, fileID uint, byteRange *DownloadByteRange) (*DownloadFileResult, error) {
	if fileID == 0 {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "file id is required")
	}
	if s.store == nil {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "file storage is not initialized")
	}

	file, err := repo.GetFileByID(ctx, fileID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.WithMessage(apperrors.ErrNotFound, "file not found")
		}
		return nil, dbOperationError(err)
	}

	if err := s.ensureDownloadAllowed(ctx, requesterID, file); err != nil {
		return nil, err
	}

	var content io.ReadCloser
	if byteRange == nil {
		content, err = s.store.Open(ctx, file.StorageKey)
	} else {
		content, err = s.store.OpenRange(ctx, file.StorageKey, byteRange.Start, byteRange.End)
	}
	if err != nil {
		return nil, err
	}

	contentType := strings.TrimSpace(file.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return &DownloadFileResult{
		OriginalName: cleanOriginalFilename(file.OriginalName),
		ContentType:  contentType,
		Size:         file.Size,
		SHA256:       file.SHA256,
		Content:      content,
	}, nil
}

func (s *fileService) ensureDownloadAllowed(ctx context.Context, requesterID uint, file *model.File) error {
	if file.UserID == requesterID {
		return nil
	}
	if file.ConversationID != nil {
		return s.ensureConversationMember(ctx, *file.ConversationID, requesterID)
	}

	allowed, err := s.isAllowedByLegacyFileMessage(ctx, requesterID, file.ID)
	if err != nil {
		return err
	}
	if !allowed {
		return apperrors.ErrPermissionDenied
	}
	return nil
}

func (s *fileService) ensureStoredSHA256(ctx context.Context, stored *storage.StoredObject, expected string) error {
	if expected == "" {
		return nil
	}
	// 比对必须发生在存储层写完之后，因为后端要以实际收到并落盘的内容为准。
	if !strings.EqualFold(expected, stored.SHA256) {
		_ = s.store.Delete(ctx, stored.Key)
		return apperrors.WithMessage(apperrors.ErrInvalidInput, "file sha256 mismatch")
	}
	return nil
}

func (s *fileService) ensureConversationMember(ctx context.Context, conversationID, requesterID uint) error {
	inConversation, err := repo.IsUserInConversation(ctx, conversationID, requesterID)
	if err != nil {
		return dbOperationError(err)
	}
	if !inConversation {
		return apperrors.ErrPermissionDenied
	}
	return nil
}

func (s *fileService) isAllowedByLegacyFileMessage(ctx context.Context, requesterID, fileID uint) (bool, error) {
	messages, err := repo.ListPotentialFileMessagesByFileID(ctx, fileID)
	if err != nil {
		return false, dbOperationError(err)
	}
	for _, message := range messages {
		linkedFileID, ok := fileIDFromMessageContent(message.Content)
		if !ok || linkedFileID != fileID {
			continue
		}
		inConversation, err := repo.IsUserInConversation(ctx, message.ConversationID, requesterID)
		if err != nil {
			return false, dbOperationError(err)
		}
		if inConversation {
			return true, nil
		}
	}
	return false, nil
}

func (s *fileService) getWritableUploadSession(ctx context.Context, userID uint, uploadID string) (*model.UploadSession, error) {
	if s.store == nil {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "file storage is not initialized")
	}
	session, err := s.getUploadSession(ctx, userID, uploadID)
	if err != nil {
		return nil, err
	}
	if session.Status != model.UploadSessionStatusPending {
		return nil, apperrors.WithMessage(apperrors.ErrConflict, "upload is not pending")
	}
	if time.Now().After(session.ExpiresAt) {
		return nil, apperrors.WithMessage(apperrors.ErrConflict, "upload session expired")
	}
	return session, nil
}

func (s *fileService) getUploadSession(ctx context.Context, userID uint, uploadID string) (*model.UploadSession, error) {
	uploadID = strings.TrimSpace(uploadID)
	if uploadID == "" {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "upload id is required")
	}
	session, err := repo.GetUploadSession(ctx, uploadID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.WithMessage(apperrors.ErrNotFound, "upload session not found")
		}
		return nil, dbOperationError(err)
	}
	if session.UserID != userID {
		return nil, apperrors.ErrPermissionDenied
	}
	return session, nil
}

func expectedChunkSize(session *model.UploadSession, index int) int64 {
	offset := int64(index) * session.ChunkSize
	remaining := session.Size - offset
	if remaining < session.ChunkSize {
		return remaining
	}
	return session.ChunkSize
}

func ensureAllChunksPresent(session *model.UploadSession, chunks []model.UploadChunk) error {
	if len(chunks) != session.TotalChunks {
		return apperrors.WithMessage(apperrors.ErrConflict, "upload chunks are incomplete")
	}
	seen := make(map[int]int64, len(chunks))
	for _, chunk := range chunks {
		seen[chunk.Index] = chunk.Size
	}
	for index := 0; index < session.TotalChunks; index++ {
		size, ok := seen[index]
		if !ok || size != expectedChunkSize(session, index) {
			return apperrors.WithMessage(apperrors.ErrConflict, "upload chunks are incomplete")
		}
	}
	return nil
}

func chunkIndexes(chunks []model.UploadChunk) []int {
	indexes := make([]int, 0, len(chunks))
	for _, chunk := range chunks {
		indexes = append(indexes, chunk.Index)
	}
	sort.Ints(indexes)
	return indexes
}

func normalizeMultipartContentType(contentType string) string {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return "application/octet-stream"
	}
	return contentType
}

func normalizeClientSHA256(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", nil
	}
	if len(value) != 64 {
		return "", apperrors.WithMessage(apperrors.ErrInvalidInput, "invalid sha256")
	}
	for _, ch := range value {
		if (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') {
			continue
		}
		return "", apperrors.WithMessage(apperrors.ErrInvalidInput, "invalid sha256")
	}
	return value, nil
}

func randomUploadID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// cleanOriginalFilename 清理客户端上传的原始文件名，仅保留基础文件名用于展示。
func cleanOriginalFilename(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if name == "" {
		return "file"
	}
	return path.Base(name)
}

// cleanStorageKey 规范化 /uploads 路由传入的路径，只保留存储层使用的相对 key。
func cleanStorageKey(key string) string {
	key = strings.TrimSpace(strings.ReplaceAll(key, "\\", "/"))
	if key == "" || strings.Contains(key, "\x00") {
		return ""
	}
	cleaned := strings.TrimLeft(path.Clean("/"+key), "/")
	if cleaned == "" || cleaned == "." {
		return ""
	}
	return cleaned
}

// uploadResponseURL 根据用途决定上传接口返回给前端的访问地址。
// 头像返回公开 /uploads 地址；聊天附件返回鉴权下载接口，避免暴露可直连的磁盘路径。
func uploadResponseURL(file *model.File) string {
	if dto.FilePurpose(file.Purpose) == dto.FilePurposeAvatar {
		return file.URL
	}
	return "/v1/file/" + strconv.FormatUint(uint64(file.ID), 10) + "/download"
}

// normalizeContentType 从 controller 传入值或 multipart header 中提取 Content-Type。
func normalizeContentType(contentType string, header *multipart.FileHeader) string {
	contentType = strings.TrimSpace(contentType)
	if contentType != "" {
		return contentType
	}
	if header == nil {
		return "application/octet-stream"
	}
	if fromHeader := strings.TrimSpace(header.Header.Get("Content-Type")); fromHeader != "" {
		return fromHeader
	}
	return "application/octet-stream"
}

// normalizeFilePurpose 在客户端未传 purpose 时默认按普通聊天文件处理。
func normalizeFilePurpose(purpose dto.FilePurpose) dto.FilePurpose {
	if purpose == "" {
		return dto.FilePurposeChatFile
	}
	return purpose
}

// isValidFilePurpose 判断上传用途是否属于服务端支持的枚举。
func isValidFilePurpose(purpose dto.FilePurpose) bool {
	switch purpose {
	case dto.FilePurposeAvatar, dto.FilePurposeChatImage, dto.FilePurposeChatFile:
		return true
	default:
		return false
	}
}

// isAllowedUpload 根据用途、扩展名和 MIME 类型判断文件是否允许上传。
func isAllowedUpload(purpose dto.FilePurpose, ext, contentType string) bool {
	if ext == "" {
		return false
	}
	if purpose == dto.FilePurposeAvatar || purpose == dto.FilePurposeChatImage {
		return isImageExt(ext) && strings.HasPrefix(contentType, "image/")
	}
	return allowedChatFileExt[ext]
}

// isImageExt 判断扩展名是否属于当前支持的图片类型。
func isImageExt(ext string) bool {
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
		return true
	default:
		return false
	}
}

// allowedChatFileExt 是普通聊天附件允许的文件扩展名白名单。
var allowedChatFileExt = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".gif":  true,
	".webp": true,
	".pdf":  true,
	".doc":  true,
	".docx": true,
	".txt":  true,
	".zip":  true,
}
