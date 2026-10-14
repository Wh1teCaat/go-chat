package service

import (
	"chat_proj/internal/repository"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"io"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"chat_proj/internal/dto"
	"chat_proj/internal/model"
	"chat_proj/internal/storage"
	"chat_proj/pkg/apperrors"
)

func TestUploadFileSavesObjectAndFileRecord(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)
	uploadRoot := t.TempDir()
	InitFileStorage(storage.NewLocalStorage(uploadRoot, "/uploads"))

	filePath := filepath.Join(t.TempDir(), "abc.pdf")
	content := []byte("pdf content")
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatalf("failed to write temp upload file: %v", err)
	}
	file, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("failed to open temp upload file: %v", err)
	}
	defer file.Close()

	result, err := FileService.UploadFile(context.Background(), UploadFileInput{
		File: file,
		Header: &multipart.FileHeader{
			Filename: "abc.pdf",
			Size:     int64(len(content)),
			Header: textproto.MIMEHeader{
				"Content-Type": []string{"application/pdf"},
			},
		},
		Purpose:     dto.FilePurposeChatFile,
		UploaderID:  7,
		ContentType: "application/pdf",
	})
	if err != nil {
		t.Fatalf("UploadFile returned error: %v", err)
	}

	if result.ID == 0 {
		t.Fatal("expected file record ID")
	}
	if result.Filename != "abc.pdf" {
		t.Fatalf("expected original filename abc.pdf, got %q", result.Filename)
	}
	if result.URL == "" {
		t.Fatal("expected public URL")
	}
	if result.Size != int64(len(content)) {
		t.Fatalf("expected size %d, got %d", len(content), result.Size)
	}
	if result.SHA256 != sha256Bytes(content) {
		t.Fatalf("expected sha256 %q, got %q", sha256Bytes(content), result.SHA256)
	}
	if result.Purpose != dto.FilePurposeChatFile {
		t.Fatalf("expected purpose %q, got %q", dto.FilePurposeChatFile, result.Purpose)
	}

	var record model.File
	if err := db.First(&record, result.ID).Error; err != nil {
		t.Fatalf("expected file record: %v", err)
	}
	if record.UserID != 7 || record.OriginalName != "abc.pdf" || record.StoredName == "abc.pdf" {
		t.Fatalf("unexpected file record: %+v", record)
	}
	if record.URL == "" || record.StorageKey == "" {
		t.Fatalf("expected stored URL/key in record, got %+v", record)
	}
	if record.SHA256 != result.SHA256 {
		t.Fatalf("expected record sha256 %q, got %q", result.SHA256, record.SHA256)
	}
	expectedDownloadURL := "/v1/file/" + fmt.Sprint(result.ID) + "/download"
	if result.URL != expectedDownloadURL {
		t.Fatalf("expected chat file response URL %q, got %q", expectedDownloadURL, result.URL)
	}
	if _, err := os.Stat(filepath.Join(uploadRoot, filepath.FromSlash(record.StorageKey))); err != nil {
		t.Fatalf("expected saved object on disk: %v", err)
	}
}

func TestUploadFileRejectsMismatchedClientSHA256(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)
	uploadRoot := t.TempDir()
	InitFileStorage(storage.NewLocalStorage(uploadRoot, "/uploads"))

	filePath := filepath.Join(t.TempDir(), "abc.pdf")
	content := []byte("pdf content")
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatalf("failed to write temp upload file: %v", err)
	}
	file, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("failed to open temp upload file: %v", err)
	}
	defer file.Close()

	_, err = FileService.UploadFile(context.Background(), UploadFileInput{
		File: file,
		Header: &multipart.FileHeader{
			Filename: "abc.pdf",
			Size:     int64(len(content)),
			Header: textproto.MIMEHeader{
				"Content-Type": []string{"application/pdf"},
			},
		},
		Purpose:      dto.FilePurposeChatFile,
		UploaderID:   7,
		ContentType:  "application/pdf",
		ClientSHA256: sha256Bytes([]byte("different content")),
	})
	if !errors.Is(err, apperrors.ErrInvalidInput) {
		t.Fatalf("expected invalid input for sha256 mismatch, got %v", err)
	}

	var count int64
	if err := db.Model(&model.File{}).Count(&count).Error; err != nil {
		t.Fatalf("failed to count file records: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no file record after sha256 mismatch, got %d", count)
	}
	if hasRegularFileOutsideParts(t, uploadRoot) {
		t.Fatal("expected saved file to be removed after sha256 mismatch")
	}
}

func TestUploadFileAllowsWordDocuments(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)
	uploadRoot := t.TempDir()
	InitFileStorage(storage.NewLocalStorage(uploadRoot, "/uploads"))

	filePath := filepath.Join(t.TempDir(), "report.docx")
	content := []byte("docx content")
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatalf("failed to write temp upload file: %v", err)
	}
	file, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("failed to open temp upload file: %v", err)
	}
	defer file.Close()

	result, err := FileService.UploadFile(context.Background(), UploadFileInput{
		File: file,
		Header: &multipart.FileHeader{
			Filename: "report.docx",
			Size:     int64(len(content)),
			Header: textproto.MIMEHeader{
				"Content-Type": []string{"application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
			},
		},
		Purpose:     dto.FilePurposeChatFile,
		UploaderID:  7,
		ContentType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	})
	if err != nil {
		t.Fatalf("UploadFile returned error: %v", err)
	}
	if result.Filename != "report.docx" || result.ID == 0 {
		t.Fatalf("unexpected upload result: %+v", result)
	}
}

func sha256Bytes(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func hasRegularFileOutsideParts(t *testing.T, root string) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && entry.Name() == ".parts" {
			return filepath.SkipDir
		}
		if !entry.IsDir() {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to walk %s: %v", root, err)
	}
	return found
}

func TestGetPublicFileAllowsAvatarOnly(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)
	uploadRoot := t.TempDir()
	InitFileStorage(storage.NewLocalStorage(uploadRoot, "/uploads"))

	avatarPath := filepath.Join(t.TempDir(), "avatar.png")
	avatarContent := []byte("avatar image")
	if err := os.WriteFile(avatarPath, avatarContent, 0644); err != nil {
		t.Fatalf("failed to write avatar file: %v", err)
	}
	avatarFile, err := os.Open(avatarPath)
	if err != nil {
		t.Fatalf("failed to open avatar file: %v", err)
	}
	defer avatarFile.Close()

	avatar, err := FileService.UploadFile(context.Background(), UploadFileInput{
		File: avatarFile,
		Header: &multipart.FileHeader{
			Filename: "avatar.png",
			Size:     int64(len(avatarContent)),
			Header: textproto.MIMEHeader{
				"Content-Type": []string{"image/png"},
			},
		},
		Purpose:     dto.FilePurposeAvatar,
		UploaderID:  7,
		ContentType: "image/png",
	})
	if err != nil {
		t.Fatalf("UploadFile avatar returned error: %v", err)
	}

	publicKey := strings.TrimPrefix(avatar.URL, "/uploads/")
	publicAvatar, err := FileService.GetPublicFile(context.Background(), publicKey)
	if err != nil {
		t.Fatalf("GetPublicFile avatar returned error: %v", err)
	}
	defer publicAvatar.Content.Close()
	data, err := io.ReadAll(publicAvatar.Content)
	if err != nil {
		t.Fatalf("ReadAll avatar returned error: %v", err)
	}
	if string(data) != string(avatarContent) {
		t.Fatalf("expected avatar content %q, got %q", string(avatarContent), string(data))
	}

	chatPath := filepath.Join(t.TempDir(), "private.pdf")
	chatContent := []byte("private pdf")
	if err := os.WriteFile(chatPath, chatContent, 0644); err != nil {
		t.Fatalf("failed to write chat file: %v", err)
	}
	chatFile, err := os.Open(chatPath)
	if err != nil {
		t.Fatalf("failed to open chat file: %v", err)
	}
	defer chatFile.Close()

	chatUpload, err := FileService.UploadFile(context.Background(), UploadFileInput{
		File: chatFile,
		Header: &multipart.FileHeader{
			Filename: "private.pdf",
			Size:     int64(len(chatContent)),
			Header: textproto.MIMEHeader{
				"Content-Type": []string{"application/pdf"},
			},
		},
		Purpose:     dto.FilePurposeChatFile,
		UploaderID:  7,
		ContentType: "application/pdf",
	})
	if err != nil {
		t.Fatalf("UploadFile chat file returned error: %v", err)
	}
	var chatRecord model.File
	if err := db.First(&chatRecord, chatUpload.ID).Error; err != nil {
		t.Fatalf("expected chat file record: %v", err)
	}
	if _, err := FileService.GetPublicFile(context.Background(), chatRecord.StorageKey); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("expected chat file public access to be hidden as not found, got %v", err)
	}
}

func TestGetDownloadFileReturnsStoredContentAndOriginalName(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)
	uploadRoot := t.TempDir()
	InitFileStorage(storage.NewLocalStorage(uploadRoot, "/uploads"))

	filePath := filepath.Join(t.TempDir(), "report.pdf")
	content := []byte("download me")
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatalf("failed to write temp upload file: %v", err)
	}
	file, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("failed to open temp upload file: %v", err)
	}
	defer file.Close()

	uploaded, err := FileService.UploadFile(context.Background(), UploadFileInput{
		File: file,
		Header: &multipart.FileHeader{
			Filename: "report.pdf",
			Size:     int64(len(content)),
			Header: textproto.MIMEHeader{
				"Content-Type": []string{"application/pdf"},
			},
		},
		Purpose:     dto.FilePurposeChatFile,
		UploaderID:  7,
		ContentType: "application/pdf",
	})
	if err != nil {
		t.Fatalf("UploadFile returned error: %v", err)
	}

	download, err := FileService.GetDownloadFile(context.Background(), 7, uploaded.ID)
	if err != nil {
		t.Fatalf("GetDownloadFile returned error: %v", err)
	}
	defer download.Content.Close()

	if download.OriginalName != "report.pdf" {
		t.Fatalf("expected original filename report.pdf, got %q", download.OriginalName)
	}
	if download.ContentType != "application/pdf" {
		t.Fatalf("expected application/pdf, got %q", download.ContentType)
	}
	if download.Size != int64(len(content)) {
		t.Fatalf("expected size %d, got %d", len(content), download.Size)
	}
	data, err := io.ReadAll(download.Content)
	if err != nil {
		t.Fatalf("ReadAll returned error: %v", err)
	}
	if string(data) != string(content) {
		t.Fatalf("expected content %q, got %q", string(content), string(data))
	}
}

func TestGetDownloadFileAllowsConversationMemberAndRejectsNonMember(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)
	uploadRoot := t.TempDir()
	InitFileStorage(storage.NewLocalStorage(uploadRoot, "/uploads"))

	uploader := createTestUser(t, db, "file-owner@test.com")
	recipient := createTestUser(t, db, "file-recipient@test.com")
	stranger := createTestUser(t, db, "file-stranger@test.com")
	conversation := model.Conversation{Type: model.ConversationTypePrivate}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	if err := db.Create(&model.ConversationMember{ConversationID: conversation.ID, UserID: uploader.ID}).Error; err != nil {
		t.Fatalf("failed to create uploader membership: %v", err)
	}
	if err := db.Create(&model.ConversationMember{ConversationID: conversation.ID, UserID: recipient.ID}).Error; err != nil {
		t.Fatalf("failed to create recipient membership: %v", err)
	}

	filePath := filepath.Join(t.TempDir(), "shared.pdf")
	content := []byte("shared content")
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatalf("failed to write temp upload file: %v", err)
	}
	file, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("failed to open temp upload file: %v", err)
	}
	defer file.Close()

	uploaded, err := FileService.UploadFile(context.Background(), UploadFileInput{
		File: file,
		Header: &multipart.FileHeader{
			Filename: "shared.pdf",
			Size:     int64(len(content)),
			Header: textproto.MIMEHeader{
				"Content-Type": []string{"application/pdf"},
			},
		},
		Purpose:     dto.FilePurposeChatFile,
		UploaderID:  uploader.ID,
		ContentType: "application/pdf",
	})
	if err != nil {
		t.Fatalf("UploadFile returned error: %v", err)
	}

	_, err = MessageService.SendConversationMessage(context.Background(), uploader.ID, dto.SendMessageInput{
		TargetType: dto.MessageTargetTypePrivate,
		TargetID:   recipient.ID,
		Content:    fmt.Sprintf(`{"kind":"file","id":%d,"filename":"shared.pdf","url":"%s"}`, uploaded.ID, uploaded.URL),
	})
	if err != nil {
		t.Fatalf("SendConversationMessage returned error: %v", err)
	}

	download, err := FileService.GetDownloadFile(context.Background(), recipient.ID, uploaded.ID)
	if err != nil {
		t.Fatalf("expected conversation member to download file, got %v", err)
	}
	defer download.Content.Close()

	data, err := io.ReadAll(download.Content)
	if err != nil {
		t.Fatalf("ReadAll returned error: %v", err)
	}
	if string(data) != string(content) {
		t.Fatalf("expected content %q, got %q", string(content), string(data))
	}

	_, err = FileService.GetDownloadFile(context.Background(), stranger.ID, uploaded.ID)
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("expected permission denied for non-member, got %v", err)
	}
}

func TestGetDownloadFileAllowsLegacyFileMessageConversationMember(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)
	uploadRoot := t.TempDir()
	InitFileStorage(storage.NewLocalStorage(uploadRoot, "/uploads"))

	uploader := createTestUser(t, db, "legacy-owner@test.com")
	recipient := createTestUser(t, db, "legacy-recipient@test.com")
	conversation := model.Conversation{Type: model.ConversationTypePrivate}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	if err := db.Create(&model.ConversationMember{ConversationID: conversation.ID, UserID: uploader.ID}).Error; err != nil {
		t.Fatalf("failed to create uploader membership: %v", err)
	}
	if err := db.Create(&model.ConversationMember{ConversationID: conversation.ID, UserID: recipient.ID}).Error; err != nil {
		t.Fatalf("failed to create recipient membership: %v", err)
	}

	filePath := filepath.Join(t.TempDir(), "legacy.pdf")
	content := []byte("legacy content")
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatalf("failed to write temp upload file: %v", err)
	}
	file, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("failed to open temp upload file: %v", err)
	}
	defer file.Close()

	uploaded, err := FileService.UploadFile(context.Background(), UploadFileInput{
		File: file,
		Header: &multipart.FileHeader{
			Filename: "legacy.pdf",
			Size:     int64(len(content)),
			Header: textproto.MIMEHeader{
				"Content-Type": []string{"application/pdf"},
			},
		},
		Purpose:     dto.FilePurposeChatFile,
		UploaderID:  uploader.ID,
		ContentType: "application/pdf",
	})
	if err != nil {
		t.Fatalf("UploadFile returned error: %v", err)
	}
	if err := db.Create(&model.Message{
		ConversationID: conversation.ID,
		SenderID:       uploader.ID,
		Content:        fmt.Sprintf(`{"kind":"file","id":%d,"filename":"legacy.pdf","url":"%s"}`, uploaded.ID, uploaded.URL),
	}).Error; err != nil {
		t.Fatalf("failed to create legacy file message: %v", err)
	}

	download, err := FileService.GetDownloadFile(context.Background(), recipient.ID, uploaded.ID)
	if err != nil {
		t.Fatalf("expected legacy conversation member to download file, got %v", err)
	}
	defer download.Content.Close()
}

func TestMultipartUploadCompletesFileRecord(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)
	uploadRoot := t.TempDir()
	InitFileStorage(storage.NewLocalStorage(uploadRoot, "/uploads"))

	initResult, err := FileService.InitMultipartUpload(context.Background(), InitMultipartUploadInput{
		Filename:    "big.txt",
		Size:        int64(len("hello world")),
		ContentType: "text/plain",
		Purpose:     dto.FilePurposeChatFile,
		ChunkSize:   6,
		UploaderID:  7,
	})
	if err != nil {
		t.Fatalf("InitMultipartUpload returned error: %v", err)
	}
	if initResult.UploadID == "" {
		t.Fatal("expected upload id")
	}
	if initResult.TotalChunks != 2 {
		t.Fatalf("expected 2 total chunks, got %d", initResult.TotalChunks)
	}

	if err := FileService.UploadMultipartChunk(context.Background(), UploadMultipartChunkInput{
		UploadID:   initResult.UploadID,
		Index:      0,
		Reader:     strings.NewReader("hello "),
		Size:       int64(len("hello ")),
		UploaderID: 7,
	}); err != nil {
		t.Fatalf("UploadMultipartChunk first returned error: %v", err)
	}
	if err := FileService.UploadMultipartChunk(context.Background(), UploadMultipartChunkInput{
		UploadID:   initResult.UploadID,
		Index:      1,
		Reader:     strings.NewReader("world"),
		Size:       int64(len("world")),
		UploaderID: 7,
	}); err != nil {
		t.Fatalf("UploadMultipartChunk second returned error: %v", err)
	}

	status, err := FileService.GetMultipartUploadStatus(context.Background(), 7, initResult.UploadID)
	if err != nil {
		t.Fatalf("GetMultipartUploadStatus returned error: %v", err)
	}
	if status.Status != model.UploadSessionStatusPending || len(status.UploadedChunks) != 2 || status.UploadedChunks[0] != 0 || status.UploadedChunks[1] != 1 {
		t.Fatalf("unexpected upload status: %+v", status)
	}

	completed, err := FileService.CompleteMultipartUpload(context.Background(), 7, initResult.UploadID)
	if err != nil {
		t.Fatalf("CompleteMultipartUpload returned error: %v", err)
	}
	if completed.ID == 0 || completed.Filename != "big.txt" || completed.Size != int64(len("hello world")) {
		t.Fatalf("unexpected completed output: %+v", completed)
	}

	var record model.File
	if err := db.First(&record, completed.ID).Error; err != nil {
		t.Fatalf("expected file record: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(uploadRoot, filepath.FromSlash(record.StorageKey)))
	if err != nil {
		t.Fatalf("expected completed file on disk: %v", err)
	}
	if string(data) != "hello world" {
		t.Fatalf("expected completed file content, got %q", string(data))
	}
}

func TestCompleteMultipartUploadRejectsMismatchedClientSHA256(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)
	uploadRoot := t.TempDir()
	InitFileStorage(storage.NewLocalStorage(uploadRoot, "/uploads"))

	initResult, err := FileService.InitMultipartUpload(context.Background(), InitMultipartUploadInput{
		Filename:    "big.txt",
		Size:        int64(len("hello world")),
		ContentType: "text/plain",
		Purpose:     dto.FilePurposeChatFile,
		ChunkSize:   6,
		UploaderID:  7,
		SHA256:      sha256Bytes([]byte("different content")),
	})
	if err != nil {
		t.Fatalf("InitMultipartUpload returned error: %v", err)
	}

	if err := FileService.UploadMultipartChunk(context.Background(), UploadMultipartChunkInput{
		UploadID:   initResult.UploadID,
		Index:      0,
		Reader:     strings.NewReader("hello "),
		Size:       int64(len("hello ")),
		UploaderID: 7,
	}); err != nil {
		t.Fatalf("UploadMultipartChunk first returned error: %v", err)
	}
	if err := FileService.UploadMultipartChunk(context.Background(), UploadMultipartChunkInput{
		UploadID:   initResult.UploadID,
		Index:      1,
		Reader:     strings.NewReader("world"),
		Size:       int64(len("world")),
		UploaderID: 7,
	}); err != nil {
		t.Fatalf("UploadMultipartChunk second returned error: %v", err)
	}

	_, err = FileService.CompleteMultipartUpload(context.Background(), 7, initResult.UploadID)
	if !errors.Is(err, apperrors.ErrInvalidInput) {
		t.Fatalf("expected invalid input for sha256 mismatch, got %v", err)
	}

	var count int64
	if err := db.Model(&model.File{}).Count(&count).Error; err != nil {
		t.Fatalf("failed to count file records: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no file record after sha256 mismatch, got %d", count)
	}
	if hasRegularFileOutsideParts(t, uploadRoot) {
		t.Fatal("expected completed file to be removed after sha256 mismatch")
	}
}

func TestCompleteMultipartUploadRejectsMissingChunk(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)
	InitFileStorage(storage.NewLocalStorage(t.TempDir(), "/uploads"))

	initResult, err := FileService.InitMultipartUpload(context.Background(), InitMultipartUploadInput{
		Filename:    "missing.txt",
		Size:        int64(len("hello world")),
		ContentType: "text/plain",
		Purpose:     dto.FilePurposeChatFile,
		ChunkSize:   6,
		UploaderID:  7,
	})
	if err != nil {
		t.Fatalf("InitMultipartUpload returned error: %v", err)
	}
	if err := FileService.UploadMultipartChunk(context.Background(), UploadMultipartChunkInput{
		UploadID:   initResult.UploadID,
		Index:      0,
		Reader:     strings.NewReader("hello "),
		Size:       int64(len("hello ")),
		UploaderID: 7,
	}); err != nil {
		t.Fatalf("UploadMultipartChunk returned error: %v", err)
	}

	_, err = FileService.CompleteMultipartUpload(context.Background(), 7, initResult.UploadID)
	if !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("expected conflict for missing chunk, got %v", err)
	}
}

func TestCleanupExpiredMultipartUploadsCancelsSessionAndDeletesParts(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)
	uploadRoot := t.TempDir()
	InitFileStorage(storage.NewLocalStorage(uploadRoot, "/uploads"))

	initResult, err := FileService.InitMultipartUpload(context.Background(), InitMultipartUploadInput{
		Filename:    "expired.txt",
		Size:        int64(len("expired")),
		ContentType: "text/plain",
		Purpose:     dto.FilePurposeChatFile,
		ChunkSize:   int64(len("expired")),
		UploaderID:  7,
	})
	if err != nil {
		t.Fatalf("InitMultipartUpload returned error: %v", err)
	}

	if err := FileService.UploadMultipartChunk(context.Background(), UploadMultipartChunkInput{
		UploadID:   initResult.UploadID,
		Index:      0,
		Reader:     strings.NewReader("expired"),
		Size:       int64(len("expired")),
		UploaderID: 7,
	}); err != nil {
		t.Fatalf("UploadMultipartChunk returned error: %v", err)
	}
	partDir := filepath.Join(uploadRoot, ".parts", initResult.UploadID)
	if _, err := os.Stat(partDir); err != nil {
		t.Fatalf("expected multipart parts directory before cleanup: %v", err)
	}

	now := time.Now()
	if err := db.Model(&model.UploadSession{}).
		Where("upload_id = ?", initResult.UploadID).
		Update("expires_at", now.Add(-time.Minute)).Error; err != nil {
		t.Fatalf("failed to expire upload session: %v", err)
	}

	cleaned, err := FileService.CleanupExpiredMultipartUploads(context.Background(), now, 10)
	if err != nil {
		t.Fatalf("CleanupExpiredMultipartUploads returned error: %v", err)
	}
	if cleaned != 1 {
		t.Fatalf("expected 1 cleaned session, got %d", cleaned)
	}

	var session model.UploadSession
	if err := db.Where("upload_id = ?", initResult.UploadID).First(&session).Error; err != nil {
		t.Fatalf("expected upload session: %v", err)
	}
	if session.Status != model.UploadSessionStatusCanceled {
		t.Fatalf("expected session status canceled, got %q", session.Status)
	}

	chunks, err := repo.ListUploadChunks(context.Background(), initResult.UploadID)
	if err != nil {
		t.Fatalf("ListUploadChunks returned error: %v", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("expected upload chunks to be deleted, got %+v", chunks)
	}
	if _, err := os.Stat(partDir); !os.IsNotExist(err) {
		t.Fatalf("expected multipart parts directory to be deleted, got %v", err)
	}
}

func TestMultipartRetryRepairsMissingProgressFromVerifiedDiskPart(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)
	root := t.TempDir()
	store := storage.NewLocalStorage(root, "/uploads")
	InitFileStorage(store)
	ctx := context.Background()
	digest := sha256Bytes([]byte("hello"))
	session, err := FileService.InitMultipartUpload(ctx, InitMultipartUploadInput{
		Filename: "retry.txt", Size: 5, ContentType: "text/plain", Purpose: dto.FilePurposeChatFile,
		ChunkSize: 5, UploaderID: 7, SHA256: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Model a successful disk write followed by a failed database registration.
	if _, err := store.SavePart(ctx, session.UploadID, 0, strings.NewReader("hello"), 5, digest); err != nil {
		t.Fatal(err)
	}
	status, err := FileService.GetMultipartUploadStatus(ctx, 7, session.UploadID)
	if err != nil || len(status.UploadedChunks) != 0 {
		t.Fatalf("unexpected initial progress: %+v %v", status, err)
	}
	for i := 0; i < 2; i++ {
		if err := FileService.UploadMultipartChunk(ctx, UploadMultipartChunkInput{
			UploadID: session.UploadID, Index: 0, Reader: strings.NewReader(""), Size: 5, UploaderID: 7, SHA256: digest,
		}); err != nil {
			t.Fatal(err)
		}
	}
	status, err = FileService.GetMultipartUploadStatus(ctx, 7, session.UploadID)
	if err != nil || len(status.UploadedChunks) != 1 {
		t.Fatalf("unexpected repaired progress: %+v %v", status, err)
	}
	completed, err := FileService.CompleteMultipartUpload(ctx, 7, session.UploadID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.SHA256 != digest {
		t.Fatalf("wrong complete digest: %s", completed.SHA256)
	}
}

// Use separate database pools against the same on-disk database, so this test
// cannot pass merely because one pool serializes access to a single connection.
func TestConcurrentMultipartCompleteReusesCommittedResult(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "uploads.db") + "?_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.UploadSession{}, &model.UploadChunk{}, &model.File{}); err != nil {
		t.Fatal(err)
	}
	initRepo(db)
	ctx := context.Background()
	root := t.TempDir()
	tracked := &countingMultipartStorage{Storage: storage.NewLocalStorage(root, "/uploads"), entered: make(chan struct{}), release: make(chan struct{})}
	first := &fileService{store: tracked}
	second := &fileService{store: tracked}
	session, err := first.InitMultipartUpload(ctx, InitMultipartUploadInput{Filename: "x.txt", Size: 5, ChunkSize: 5, Purpose: dto.FilePurposeChatFile, UploaderID: 7, SHA256: sha256Bytes([]byte("hello"))})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.UploadMultipartChunk(ctx, UploadMultipartChunkInput{UploadID: session.UploadID, Reader: strings.NewReader("hello"), Size: 5, UploaderID: 7}); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		file *dto.UploadFileOutput
		err  error
	}
	results := make(chan outcome, 2)
	go func() { f, e := first.CompleteMultipartUpload(ctx, 7, session.UploadID); results <- outcome{f, e} }()
	select {
	case <-tracked.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("merge did not start")
	}
	// While the first transaction owns the lock, an independent connection must
	// wait for the transaction instead of changing the session.
	other, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	otherRepo := repository.NewRepository(other)
	locked := make(chan error, 1)
	go func() {
		locked <- otherRepo.WithTransaction(func(tx *repository.Repository) error { _, e := tx.LockUploadSession(ctx, session.UploadID); return e })
	}()
	go func() { f, e := second.CompleteMultipartUpload(ctx, 7, session.UploadID); results <- outcome{f, e} }()
	select {
	case err := <-locked:
		t.Fatalf("lock was not held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(tracked.release)
	var id uint
	for i := 0; i < 2; i++ {
		select {
		case r := <-results:
			if r.err != nil {
				t.Fatal(r.err)
			}
			if i == 0 {
				id = r.file.ID
			} else if id != r.file.ID {
				t.Fatalf("different results: %d / %d", id, r.file.ID)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("completion timed out")
		}
	}
	if err := <-locked; err != nil {
		t.Fatal(err)
	}
	replay, err := second.CompleteMultipartUpload(ctx, 7, session.UploadID)
	if err != nil || replay.ID != id {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	if tracked.merges.Load() != 1 || tracked.cleanups.Load() != 1 {
		t.Fatalf("merge=%d cleanup=%d", tracked.merges.Load(), tracked.cleanups.Load())
	}
	var count int64
	if err := db.Model(&model.File{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("file count %d: %v", count, err)
	}
	if err := second.CancelMultipartUpload(ctx, 7, session.UploadID); !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("cancel completed upload: %v", err)
	}
	if _, err := second.CompleteMultipartUpload(ctx, 8, session.UploadID); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("unauthorized replay: %v", err)
	}
}

type countingMultipartStorage struct {
	storage.Storage
	merges   atomic.Int32
	cleanups atomic.Int32
	entered  chan struct{}
	release  chan struct{}
}

func (s *countingMultipartStorage) CompleteMultipart(ctx context.Context, input storage.CompleteMultipartInput) (*storage.StoredObject, error) {
	if s.merges.Add(1) == 1 {
		close(s.entered)
		<-s.release
	}
	return s.Storage.CompleteMultipart(ctx, input)
}
func (s *countingMultipartStorage) DeleteMultipart(ctx context.Context, id string) error {
	s.cleanups.Add(1)
	return s.Storage.DeleteMultipart(ctx, id)
}

func TestMultipartCompletionRegistrationFailureRollsBackAndRetries(t *testing.T) {
	db := setupTestDB(t)
	initRepo(db)
	root := t.TempDir()
	svc := &fileService{store: storage.NewLocalStorage(root, "/uploads")}
	ctx := context.Background()
	session, err := svc.InitMultipartUpload(ctx, InitMultipartUploadInput{Filename: "x.txt", Size: 5, ChunkSize: 5, Purpose: dto.FilePurposeChatFile, UploaderID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.UploadMultipartChunk(ctx, UploadMultipartChunkInput{UploadID: session.UploadID, Reader: strings.NewReader("hello"), Size: 5, UploaderID: 7}); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Update().Before("gorm:update").Register("test:reject_completion", func(tx *gorm.DB) {
		if values, ok := tx.Statement.Dest.(map[string]interface{}); ok && values["file_id"] != nil {
			tx.AddError(errors.New("injected completion failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteMultipartUpload(ctx, 7, session.UploadID); err == nil {
		t.Fatal("expected registration failure")
	}
	if err := db.Callback().Update().Remove("test:reject_completion"); err != nil {
		t.Fatal(err)
	}
	current, err := repo.GetUploadSession(ctx, session.UploadID)
	if err != nil || current.Status != model.UploadSessionStatusPending || current.FileID != nil {
		t.Fatalf("session not rolled back: %+v %v", current, err)
	}
	var count int64
	if err := db.Model(&model.File{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("file record not rolled back: %d %v", count, err)
	}
	var files []string
	if err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			files = append(files, p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || filepath.Base(files[0]) != "000000.part" {
		t.Fatalf("unexpected files after failure: %v", files)
	}
	if _, err := svc.CompleteMultipartUpload(ctx, 7, session.UploadID); err != nil {
		t.Fatal(err)
	}
}
