package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"chat_proj/pkg/apperrors"
)

// LocalStorage 把上传文件保存到本地磁盘。
// rootDir 是磁盘根目录，publicURL 是生成文件访问路径时使用的 URL 前缀。
type LocalStorage struct {
	rootDir   string
	publicURL string
}

// NewLocalStorage 创建本地文件存储实现。
// 例如 rootDir=uploads、publicURL=/uploads 时，key 会映射到 uploads/<key> 和 /uploads/<key>。
// 这个 URL 能不能公开访问不由存储层决定，头像和聊天附件的权限差异在 service/router 层处理。
func NewLocalStorage(rootDir, publicURL string) *LocalStorage {
	return &LocalStorage{
		rootDir:   rootDir,
		publicURL: strings.TrimRight(publicURL, "/"),
	}
}

// Save 将 Reader 中的内容写入 rootDir 下按日期划分的目录。
// 保存名由服务端随机生成，只保留已校验过的扩展名，避免重名覆盖和路径穿越。
func (s *LocalStorage) Save(ctx context.Context, input SaveObjectInput) (*StoredObject, error) {
	if input.Reader == nil {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "file content is required")
	}
	if strings.TrimSpace(s.rootDir) == "" {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "storage root directory is required")
	}

	ext := normalizeExtension(input.Meta.Extension)
	dateDir := time.Now().Format("2006/01/02")
	absDir := filepath.Join(s.rootDir, filepath.FromSlash(dateDir))
	if err := os.MkdirAll(absDir, 0755); err != nil {
		return nil, apperrors.WithCause(apperrors.ErrDBOperation, "failed to create upload directory", err)
	}

	var lastErr error
	for i := 0; i < 3; i++ {
		select {
		case <-ctx.Done():
			return nil, apperrors.WithCause(apperrors.ErrInvalidInput, "upload canceled", ctx.Err())
		default:
		}

		storedName, err := randomStoredName(ext)
		if err != nil {
			return nil, apperrors.WithCause(apperrors.ErrDBOperation, "failed to generate stored filename", err)
		}

		// O_WRONLY: 只写模式
		// O_CREATE: 文件不存在则创建
		// O_EXCL: 文件已存在则报错，避免覆盖
		absPath := filepath.Join(absDir, storedName)
		dst, err := os.OpenFile(absPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			lastErr = err
			continue
		}

		// 写入磁盘的同时计算内容哈希，避免为了完整性校验再读一遍文件。
		hasher := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(dst, hasher), input.Reader)
		closeErr := dst.Close()
		if copyErr != nil || closeErr != nil {
			_ = os.Remove(absPath)
			if copyErr != nil {
				return nil, apperrors.WithCause(apperrors.ErrDBOperation, "failed to save uploaded file", copyErr)
			}
			return nil, apperrors.WithCause(apperrors.ErrDBOperation, "failed to close uploaded file", closeErr)
		}

		key := path.Join(dateDir, storedName)
		return &StoredObject{
			Key:         key,
			URL:         s.PublicURL(key),
			StoredName:  storedName,
			Size:        written,
			ContentType: input.Meta.ContentType,
			SHA256:      hex.EncodeToString(hasher.Sum(nil)),
		}, nil
	}

	return nil, apperrors.WithCause(apperrors.ErrDBOperation, "failed to allocate stored filename", lastErr)
}

// Open 根据存储 key 打开本地文件，供下载接口流式返回文件内容。
func (s *LocalStorage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	select {
	case <-ctx.Done():
		return nil, apperrors.WithCause(apperrors.ErrInvalidInput, "open canceled", ctx.Err())
	default:
	}

	absPath, err := s.objectPath(key)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, apperrors.WithCause(apperrors.ErrNotFound, "file content not found", err)
		}
		return nil, apperrors.WithCause(apperrors.ErrDBOperation, "failed to open stored file", err)
	}
	return file, nil
}

// OpenRange 根据存储 key 打开指定字节区间，start/end 都是包含边界。
func (s *LocalStorage) OpenRange(ctx context.Context, key string, start, end int64) (io.ReadCloser, error) {
	if start < 0 || end < start {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "invalid byte range")
	}
	file, err := s.Open(ctx, key)
	if err != nil {
		return nil, err
	}

	seeker, ok := file.(io.Seeker)
	if !ok {
		_ = file.Close()
		return nil, apperrors.WithMessage(apperrors.ErrDBOperation, "stored file does not support seeking")
	}
	if _, err := seeker.Seek(start, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, apperrors.WithCause(apperrors.ErrDBOperation, "failed to seek stored file", err)
	}
	return &rangeReadCloser{
		reader: io.LimitReader(file, end-start+1),
		closer: file,
	}, nil
}

type rangeReadCloser struct {
	reader io.Reader
	closer io.Closer
}

func (r *rangeReadCloser) Read(p []byte) (int, error) {
	return r.reader.Read(p)
}

func (r *rangeReadCloser) Close() error {
	return r.closer.Close()
}

// SavePart 保存一次分片上传的临时分片，重复上传同一分片时覆盖旧内容。
func (s *LocalStorage) SavePart(ctx context.Context, uploadID string, index int, reader io.Reader) (int64, error) {
	if reader == nil {
		return 0, apperrors.WithMessage(apperrors.ErrInvalidInput, "chunk content is required")
	}
	if index < 0 {
		return 0, apperrors.WithMessage(apperrors.ErrInvalidInput, "invalid chunk index")
	}
	select {
	case <-ctx.Done():
		return 0, apperrors.WithCause(apperrors.ErrInvalidInput, "chunk upload canceled", ctx.Err())
	default:
	}

	dir, err := s.multipartDir(uploadID)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return 0, apperrors.WithCause(apperrors.ErrDBOperation, "failed to create multipart directory", err)
	}

	partPath := filepath.Join(dir, partFilename(index))
	dst, err := os.OpenFile(partPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return 0, apperrors.WithCause(apperrors.ErrDBOperation, "failed to create chunk file", err)
	}
	written, copyErr := io.Copy(dst, reader)
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(partPath)
		if copyErr != nil {
			return 0, apperrors.WithCause(apperrors.ErrDBOperation, "failed to save chunk file", copyErr)
		}
		return 0, apperrors.WithCause(apperrors.ErrDBOperation, "failed to close chunk file", closeErr)
	}
	return written, nil
}

// CompleteMultipart 按分片 index 顺序合并临时分片，生成正式存储对象。
func (s *LocalStorage) CompleteMultipart(ctx context.Context, input CompleteMultipartInput) (*StoredObject, error) {
	if input.TotalChunks <= 0 {
		return nil, apperrors.WithMessage(apperrors.ErrInvalidInput, "total chunks is required")
	}
	partDir, err := s.multipartDir(input.UploadID)
	if err != nil {
		return nil, err
	}

	ext := normalizeExtension(input.Meta.Extension)
	dateDir := time.Now().Format("2006/01/02")
	absDir := filepath.Join(s.rootDir, filepath.FromSlash(dateDir))
	if err := os.MkdirAll(absDir, 0755); err != nil {
		return nil, apperrors.WithCause(apperrors.ErrDBOperation, "failed to create upload directory", err)
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		select {
		case <-ctx.Done():
			return nil, apperrors.WithCause(apperrors.ErrInvalidInput, "complete upload canceled", ctx.Err())
		default:
		}

		storedName, err := randomStoredName(ext)
		if err != nil {
			return nil, apperrors.WithCause(apperrors.ErrDBOperation, "failed to generate stored filename", err)
		}
		absPath := filepath.Join(absDir, storedName)
		dst, err := os.OpenFile(absPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			lastErr = err
			continue
		}

		written, sha256Hex, err := copyParts(dst, partDir, input.TotalChunks)
		closeErr := dst.Close()
		if err != nil || closeErr != nil {
			_ = os.Remove(absPath)
			if err != nil {
				return nil, err
			}
			return nil, apperrors.WithCause(apperrors.ErrDBOperation, "failed to close completed file", closeErr)
		}

		key := path.Join(dateDir, storedName)
		return &StoredObject{
			Key:         key,
			URL:         s.PublicURL(key),
			StoredName:  storedName,
			Size:        written,
			ContentType: input.Meta.ContentType,
			SHA256:      sha256Hex,
		}, nil
	}
	return nil, apperrors.WithCause(apperrors.ErrDBOperation, "failed to allocate stored filename", lastErr)
}

// DeleteMultipart 删除一次分片上传的临时目录。
func (s *LocalStorage) DeleteMultipart(ctx context.Context, uploadID string) error {
	select {
	case <-ctx.Done():
		return apperrors.WithCause(apperrors.ErrInvalidInput, "delete multipart canceled", ctx.Err())
	default:
	}
	dir, err := s.multipartDir(uploadID)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// Delete 根据存储 key 删除本地文件，主要用于业务回滚或后续删除接口。
func (s *LocalStorage) Delete(ctx context.Context, key string) error {
	select {
	case <-ctx.Done():
		return apperrors.WithCause(apperrors.ErrInvalidInput, "delete canceled", ctx.Err())
	default:
	}

	absPath, err := s.objectPath(key)
	if err != nil {
		return err
	}
	return os.Remove(absPath)
}

// PublicURL 把内部存储 key 转成 URL 路径。
// 调用方仍需要根据文件用途决定是否把这个 URL 当公开地址返回给前端。
func (s *LocalStorage) PublicURL(key string) string {
	cleanKey := strings.TrimLeft(path.Clean("/"+key), "/")
	if s.publicURL == "" {
		return "/" + cleanKey
	}
	return s.publicURL + "/" + cleanKey
}

// objectPath 把存储 key 转成 rootDir 下的绝对文件路径，并拒绝异常 key。
func (s *LocalStorage) objectPath(key string) (string, error) {
	cleanKey := path.Clean("/" + key)
	if cleanKey == "/" || strings.Contains(cleanKey, "..") {
		return "", apperrors.WithMessage(apperrors.ErrInvalidInput, "invalid storage key")
	}
	return filepath.Join(s.rootDir, filepath.FromSlash(strings.TrimPrefix(cleanKey, "/"))), nil
}

func (s *LocalStorage) multipartDir(uploadID string) (string, error) {
	cleanID := strings.Trim(path.Clean("/"+uploadID), "/")
	if cleanID == "" || strings.Contains(cleanID, "..") || strings.Contains(cleanID, "/") {
		return "", apperrors.WithMessage(apperrors.ErrInvalidInput, "invalid upload id")
	}
	return filepath.Join(s.rootDir, ".parts", cleanID), nil
}

func partFilename(index int) string {
	return fmt.Sprintf("%06d.part", index)
}

func copyParts(dst io.Writer, partDir string, totalChunks int) (int64, string, error) {
	var written int64
	hasher := sha256.New()
	writer := io.MultiWriter(dst, hasher)
	for index := 0; index < totalChunks; index++ {
		partPath := filepath.Join(partDir, partFilename(index))
		src, err := os.Open(partPath)
		if err != nil {
			if os.IsNotExist(err) {
				return 0, "", apperrors.WithCause(apperrors.ErrNotFound, "upload chunk not found", err)
			}
			return 0, "", apperrors.WithCause(apperrors.ErrDBOperation, "failed to open chunk file", err)
		}
		// 分片按顺序写入正式文件，并对合并后的完整内容计算 SHA-256。
		n, copyErr := io.Copy(writer, src)
		closeErr := src.Close()
		if copyErr != nil || closeErr != nil {
			if copyErr != nil {
				return 0, "", apperrors.WithCause(apperrors.ErrDBOperation, "failed to merge chunk file", copyErr)
			}
			return 0, "", apperrors.WithCause(apperrors.ErrDBOperation, "failed to close chunk file", closeErr)
		}
		written += n
	}
	return written, hex.EncodeToString(hasher.Sum(nil)), nil
}

// normalizeExtension 规范化扩展名，确保扩展名不会携带目录片段。
func normalizeExtension(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" {
		return ""
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	// 后缀只保留路径基础名，避免把用户输入拼进目录结构。
	return filepath.Ext("file" + ext)
}

// randomStoredName 生成服务端存储文件名，避免使用用户原始文件名落盘。
func randomStoredName(ext string) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf) + ext, nil
}
