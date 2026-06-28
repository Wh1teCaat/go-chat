package controller

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"chat_proj/internal/dto"
	"chat_proj/internal/service"
	"chat_proj/pkg/apperrors"
	"chat_proj/pkg/response"

	"github.com/gin-gonic/gin"
)

// UploadFile 处理 multipart/form-data 上传请求。
// 客户端只传 file 和可选 purpose；上传者身份从鉴权中间件注入的 user_id 获取。
func UploadFile(c *gin.Context) {
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		response.BindError(c, err)
		return
	}
	defer file.Close()

	result, err := service.FileService.UploadFile(c.Request.Context(), service.UploadFileInput{
		File:         file,
		Header:       header,
		Purpose:      dto.FilePurpose(c.PostForm("purpose")),
		UploaderID:   userID(c),
		ContentType:  header.Header.Get("Content-Type"),
		ClientSHA256: c.PostForm("sha256"),
	})
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

// PublicUploadedFile 服务公开上传文件，目前只用于头像。
// 这里不直接映射磁盘目录，而是交给 service 查库判断 purpose，避免聊天附件被公开 URL 绕过鉴权。
func PublicUploadedFile(c *gin.Context) {
	result, err := service.FileService.GetPublicFile(c.Request.Context(), c.Param("filepath"))
	if err != nil {
		response.Error(c, err)
		return
	}
	defer result.Content.Close()

	headers := map[string]string{
		"Cache-Control": "public, max-age=86400",
	}
	if result.SHA256 != "" {
		headers["X-Content-SHA256"] = result.SHA256
	}
	c.DataFromReader(http.StatusOK, result.Size, result.ContentType, result.Content, headers)
}

// InitMultipartUpload 创建分片上传会话，返回 uploadID 和分片参数。
func InitMultipartUpload(c *gin.Context) {
	var input dto.InitMultipartUploadInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BindError(c, err)
		return
	}

	result, err := service.FileService.InitMultipartUpload(c.Request.Context(), service.InitMultipartUploadInput{
		Filename:    input.Filename,
		Size:        input.Size,
		ContentType: input.ContentType,
		Purpose:     input.Purpose,
		ChunkSize:   input.ChunkSize,
		UploaderID:  userID(c),
		SHA256:      input.SHA256,
	})
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

// UploadMultipartChunk 保存一个分片；重复上传同一 index 会覆盖旧分片。
func UploadMultipartChunk(c *gin.Context) {
	index, err := strconv.Atoi(c.Param("index"))
	if err != nil || index < 0 {
		response.Error(c, apperrors.WithMessage(apperrors.ErrInvalidInput, "invalid chunk index"))
		return
	}
	file, header, err := c.Request.FormFile("chunk")
	if err != nil {
		response.BindError(c, err)
		return
	}
	defer file.Close()

	err = service.FileService.UploadMultipartChunk(c.Request.Context(), service.UploadMultipartChunkInput{
		UploadID:   c.Param("uploadID"),
		Index:      index,
		Reader:     file,
		Size:       header.Size,
		UploaderID: userID(c),
	})
	if err != nil {
		response.Error(c, err)
		return
	}
	response.Message(c, "chunk uploaded")
}

// GetMultipartUploadStatus 返回已上传分片列表，用于断点续传时跳过已有分片。
func GetMultipartUploadStatus(c *gin.Context) {
	result, err := service.FileService.GetMultipartUploadStatus(c.Request.Context(), userID(c), c.Param("uploadID"))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

// CompleteMultipartUpload 合并所有分片并创建 files 记录。
func CompleteMultipartUpload(c *gin.Context) {
	result, err := service.FileService.CompleteMultipartUpload(c.Request.Context(), userID(c), c.Param("uploadID"))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

// CancelMultipartUpload 取消上传会话并清理临时分片。
func CancelMultipartUpload(c *gin.Context) {
	if err := service.FileService.CancelMultipartUpload(c.Request.Context(), userID(c), c.Param("uploadID")); err != nil {
		response.Error(c, err)
		return
	}
	response.Message(c, "upload canceled")
}

// DownloadFile 根据文件 ID 下载原文件内容。
// 文件名使用数据库里的 OriginalName，真实磁盘路径只通过 service/storage 的 storage_key 间接访问。
func DownloadFile(c *gin.Context) {
	fileID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || fileID == 0 {
		response.Error(c, apperrors.WithMessage(apperrors.ErrInvalidInput, "invalid file id"))
		return
	}

	result, err := service.FileService.GetDownloadFile(c.Request.Context(), userID(c), uint(fileID))
	if err != nil {
		response.Error(c, err)
		return
	}

	headers := map[string]string{
		"Accept-Ranges":       "bytes",
		"Content-Disposition": attachmentDisposition(result.OriginalName),
	}
	if result.SHA256 != "" {
		headers["X-Content-SHA256"] = result.SHA256
	}

	byteRange, hasRange, rangeErr := parseRangeHeader(c.GetHeader("Range"), result.Size)
	if rangeErr != nil {
		_ = result.Content.Close()
		c.Header("Accept-Ranges", "bytes")
		c.Header("Content-Range", fmt.Sprintf("bytes */%d", result.Size))
		c.Status(http.StatusRequestedRangeNotSatisfiable)
		return
	}

	if hasRange {
		_ = result.Content.Close()
		result, err = service.FileService.GetDownloadFileRange(c.Request.Context(), userID(c), uint(fileID), service.DownloadByteRange{
			Start: byteRange.start,
			End:   byteRange.end,
		})
		if err != nil {
			response.Error(c, err)
			return
		}
		defer result.Content.Close()

		headers["Content-Range"] = fmt.Sprintf("bytes %d-%d/%d", byteRange.start, byteRange.end, result.Size)
		if result.SHA256 != "" {
			headers["X-Content-SHA256"] = result.SHA256
		}
		c.DataFromReader(http.StatusPartialContent, byteRange.length(), result.ContentType, result.Content, headers)
		return
	}

	defer result.Content.Close()
	c.DataFromReader(http.StatusOK, result.Size, result.ContentType, result.Content, headers)
}

// attachmentDisposition 生成下载响应头，让浏览器用用户原始文件名保存文件。
func attachmentDisposition(filename string) string {
	escaped := strings.ReplaceAll(filename, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return fmt.Sprintf(`attachment; filename="%s"`, escaped)
}

type parsedByteRange struct {
	start int64
	end   int64
}

func (r parsedByteRange) length() int64 {
	return r.end - r.start + 1
}

func parseRangeHeader(value string, size int64) (parsedByteRange, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return parsedByteRange{}, false, nil
	}
	if size <= 0 || !strings.HasPrefix(value, "bytes=") {
		return parsedByteRange{}, false, fmt.Errorf("invalid range")
	}

	spec := strings.TrimPrefix(value, "bytes=")
	if strings.Contains(spec, ",") {
		return parsedByteRange{}, false, fmt.Errorf("multiple ranges are not supported")
	}

	startPart, endPart, ok := strings.Cut(spec, "-")
	if !ok {
		return parsedByteRange{}, false, fmt.Errorf("invalid range")
	}

	if startPart == "" {
		suffixLength, err := strconv.ParseInt(endPart, 10, 64)
		if err != nil || suffixLength <= 0 {
			return parsedByteRange{}, false, fmt.Errorf("invalid suffix range")
		}
		if suffixLength >= size {
			return parsedByteRange{start: 0, end: size - 1}, true, nil
		}
		return parsedByteRange{start: size - suffixLength, end: size - 1}, true, nil
	}

	start, err := strconv.ParseInt(startPart, 10, 64)
	if err != nil || start < 0 || start >= size {
		return parsedByteRange{}, false, fmt.Errorf("invalid range start")
	}

	if endPart == "" {
		return parsedByteRange{start: start, end: size - 1}, true, nil
	}

	end, err := strconv.ParseInt(endPart, 10, 64)
	if err != nil || end < start {
		return parsedByteRange{}, false, fmt.Errorf("invalid range end")
	}
	if end >= size {
		end = size - 1
	}
	return parsedByteRange{start: start, end: end}, true, nil
}
