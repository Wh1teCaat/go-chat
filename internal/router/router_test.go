package router

import (
	"bytes"
	"chat_proj/internal/auth"
	"chat_proj/internal/dto"
	"chat_proj/internal/model"
	"chat_proj/internal/repository"
	"chat_proj/internal/service"
	"chat_proj/internal/storage"
	"chat_proj/pkg/logger"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMain(m *testing.M) {
	logger.Logger = zap.NewNop()
	os.Exit(m.Run())
}

func sha256String(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func TestLoginTokenIsInjectedIntoAddFriend(t *testing.T) {
	if err := auth.Init("router-chain-test-secret"); err != nil {
		t.Fatalf("auth.Init returned error: %v", err)
	}

	db := setupRouterTestDB(t)
	service.Init(repository.NewRepository(db))
	r := New()

	register(t, r, "alice@example.com", "password123")
	register(t, r, "bob@example.com", "password123")

	var alice model.User
	if err := db.Where("email = ?", "alice@example.com").First(&alice).Error; err != nil {
		t.Fatalf("failed to query alice: %v", err)
	}
	var bob model.User
	if err := db.Where("email = ?", "bob@example.com").First(&bob).Error; err != nil {
		t.Fatalf("failed to query bob: %v", err)
	}

	token := login(t, r, "alice@example.com", "password123")
	body := bytes.NewBufferString(`{"friendEmail":"bob@example.com"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/friend/add", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected add friend status 200, got %d: %s", w.Code, w.Body.String())
	}

	var relation model.FriendRelation
	if err := db.Where("user_id = ? AND friend_id = ?", alice.ID, bob.ID).First(&relation).Error; err != nil {
		t.Fatalf("failed to find friend relation created by injected user_id: %v", err)
	}
	if relation.Status != model.FriendRelationStatusPending {
		t.Fatalf("expected pending relation, got %s", relation.Status)
	}
}

func TestAddFriendByEmailRoute(t *testing.T) {
	if err := auth.Init("router-add-friend-email-test-secret"); err != nil {
		t.Fatalf("auth.Init returned error: %v", err)
	}

	db := setupRouterTestDB(t)
	service.Init(repository.NewRepository(db))
	r := New()

	register(t, r, "email-alice@example.com", "password123")
	register(t, r, "email-bob@example.com", "password123")

	var alice model.User
	if err := db.Where("email = ?", "email-alice@example.com").First(&alice).Error; err != nil {
		t.Fatalf("failed to query alice: %v", err)
	}
	var bob model.User
	if err := db.Where("email = ?", "email-bob@example.com").First(&bob).Error; err != nil {
		t.Fatalf("failed to query bob: %v", err)
	}

	token := login(t, r, "email-alice@example.com", "password123")
	body := bytes.NewBufferString(`{"friendEmail":"email-bob@example.com"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/friend/add", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected add friend status 200, got %d: %s", w.Code, w.Body.String())
	}

	var relation model.FriendRelation
	if err := db.Where("user_id = ? AND friend_id = ?", alice.ID, bob.ID).First(&relation).Error; err != nil {
		t.Fatalf("failed to find friend relation created by email: %v", err)
	}
}

func TestRouterSetsRequestIDHeader(t *testing.T) {
	r := New()

	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Header().Get("X-Request-ID") == "" {
		t.Fatal("expected X-Request-ID header")
	}
}

func TestUploadFileRouteSavesAuthenticatedUserFile(t *testing.T) {
	if err := auth.Init("router-upload-file-test-secret"); err != nil {
		t.Fatalf("auth.Init returned error: %v", err)
	}

	db := setupRouterTestDB(t)
	service.Init(repository.NewRepository(db))
	service.InitFileStorage(storage.NewLocalStorage(t.TempDir(), "/uploads"))
	r := New()

	register(t, r, "upload@example.com", "password123")
	token := login(t, r, "upload@example.com", "password123")

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "abc.pdf")
	if err != nil {
		t.Fatalf("CreateFormFile returned error: %v", err)
	}
	if _, err := part.Write([]byte("pdf content")); err != nil {
		t.Fatalf("failed to write multipart file: %v", err)
	}
	if err := writer.WriteField("purpose", string(dto.FilePurposeChatFile)); err != nil {
		t.Fatalf("WriteField returned error: %v", err)
	}
	if err := writer.WriteField("sha256", sha256String("pdf content")); err != nil {
		t.Fatalf("WriteField returned error: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/file/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected upload status 200, got %d: %s", w.Code, w.Body.String())
	}

	var response struct {
		Code int `json:"code"`
		Data struct {
			ID       uint   `json:"id"`
			URL      string `json:"url"`
			Filename string `json:"filename"`
			SHA256   string `json:"sha256"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode upload response: %v", err)
	}
	expectedDownloadURL := "/v1/file/" + strconv.FormatUint(uint64(response.Data.ID), 10) + "/download"
	if response.Code != 0 || response.Data.ID == 0 || response.Data.Filename != "abc.pdf" || response.Data.URL != expectedDownloadURL {
		t.Fatalf("unexpected upload response: %+v", response)
	}
	if response.Data.SHA256 != sha256String("pdf content") {
		t.Fatalf("expected sha256 %q, got %q", sha256String("pdf content"), response.Data.SHA256)
	}

	var uploaded model.File
	if err := db.First(&uploaded, response.Data.ID).Error; err != nil {
		t.Fatalf("expected uploaded file record: %v", err)
	}
	if uploaded.OriginalName != "abc.pdf" {
		t.Fatalf("expected original filename abc.pdf, got %q", uploaded.OriginalName)
	}
}

func TestPublicUploadsServeAvatarAndHideChatFile(t *testing.T) {
	if err := auth.Init("router-public-upload-test-secret"); err != nil {
		t.Fatalf("auth.Init returned error: %v", err)
	}

	db := setupRouterTestDB(t)
	service.Init(repository.NewRepository(db))
	service.InitFileStorage(storage.NewLocalStorage(t.TempDir(), "/uploads"))
	r := New()

	register(t, r, "public-file@example.com", "password123")
	token := login(t, r, "public-file@example.com", "password123")

	upload := func(filename, contentType, purpose, content string) (uint, string) {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		fileHeader := textproto.MIMEHeader{}
		fileHeader.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
		fileHeader.Set("Content-Type", contentType)
		part, err := writer.CreatePart(fileHeader)
		if err != nil {
			t.Fatalf("CreatePart returned error: %v", err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatalf("failed to write multipart file: %v", err)
		}
		if err := writer.WriteField("purpose", purpose); err != nil {
			t.Fatalf("WriteField returned error: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("failed to close multipart writer: %v", err)
		}

		req := httptest.NewRequest(http.MethodPost, "/v1/file/upload", &body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected upload status 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Data struct {
				ID  uint   `json:"id"`
				URL string `json:"url"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("failed to decode upload response: %v", err)
		}
		return response.Data.ID, response.Data.URL
	}

	_, avatarURL := upload("avatar.png", "image/png", string(dto.FilePurposeAvatar), "avatar image")
	if !strings.HasPrefix(avatarURL, "/uploads/") {
		t.Fatalf("expected avatar upload to return public uploads URL, got %q", avatarURL)
	}
	avatarReq := httptest.NewRequest(http.MethodGet, avatarURL, nil)
	avatarW := httptest.NewRecorder()
	r.ServeHTTP(avatarW, avatarReq)
	if avatarW.Code != http.StatusOK {
		t.Fatalf("expected public avatar status 200, got %d: %s", avatarW.Code, avatarW.Body.String())
	}
	if avatarW.Body.String() != "avatar image" {
		t.Fatalf("expected avatar content, got %q", avatarW.Body.String())
	}

	chatID, chatURL := upload("private.pdf", "application/pdf", string(dto.FilePurposeChatFile), "private pdf")
	if chatURL != "/v1/file/"+strconv.FormatUint(uint64(chatID), 10)+"/download" {
		t.Fatalf("expected chat upload to return download URL, got %q", chatURL)
	}
	var chatRecord model.File
	if err := db.First(&chatRecord, chatID).Error; err != nil {
		t.Fatalf("expected chat file record: %v", err)
	}
	privateReq := httptest.NewRequest(http.MethodGet, "/uploads/"+chatRecord.StorageKey, nil)
	privateW := httptest.NewRecorder()
	r.ServeHTTP(privateW, privateReq)
	if privateW.Code != http.StatusNotFound {
		t.Fatalf("expected private chat file direct URL status 404, got %d: %s", privateW.Code, privateW.Body.String())
	}
}

func TestDownloadFileRouteReturnsOriginalFilenameAndContent(t *testing.T) {
	if err := auth.Init("router-download-file-test-secret"); err != nil {
		t.Fatalf("auth.Init returned error: %v", err)
	}

	db := setupRouterTestDB(t)
	service.Init(repository.NewRepository(db))
	service.InitFileStorage(storage.NewLocalStorage(t.TempDir(), "/uploads"))
	r := New()

	register(t, r, "download@example.com", "password123")
	token := login(t, r, "download@example.com", "password123")

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fileHeader := textproto.MIMEHeader{}
	fileHeader.Set("Content-Disposition", `form-data; name="file"; filename="report.pdf"`)
	fileHeader.Set("Content-Type", "application/pdf")
	part, err := writer.CreatePart(fileHeader)
	if err != nil {
		t.Fatalf("CreatePart returned error: %v", err)
	}
	if _, err := part.Write([]byte("pdf content")); err != nil {
		t.Fatalf("failed to write multipart file: %v", err)
	}
	if err := writer.WriteField("purpose", string(dto.FilePurposeChatFile)); err != nil {
		t.Fatalf("WriteField returned error: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	uploadReq := httptest.NewRequest(http.MethodPost, "/v1/file/upload", &body)
	uploadReq.Header.Set("Content-Type", writer.FormDataContentType())
	uploadReq.Header.Set("Authorization", "Bearer "+token)
	uploadW := httptest.NewRecorder()
	r.ServeHTTP(uploadW, uploadReq)
	if uploadW.Code != http.StatusOK {
		t.Fatalf("expected upload status 200, got %d: %s", uploadW.Code, uploadW.Body.String())
	}

	var uploadResponse struct {
		Data struct {
			ID     uint   `json:"id"`
			SHA256 string `json:"sha256"`
		} `json:"data"`
	}
	if err := json.Unmarshal(uploadW.Body.Bytes(), &uploadResponse); err != nil {
		t.Fatalf("failed to decode upload response: %v", err)
	}
	expectedHash := sha256String("pdf content")
	if uploadResponse.Data.SHA256 != expectedHash {
		t.Fatalf("expected upload sha256 %q, got %q", expectedHash, uploadResponse.Data.SHA256)
	}

	downloadReq := httptest.NewRequest(http.MethodGet, "/v1/file/"+strconv.FormatUint(uint64(uploadResponse.Data.ID), 10)+"/download", nil)
	downloadReq.Header.Set("Authorization", "Bearer "+token)
	downloadW := httptest.NewRecorder()

	r.ServeHTTP(downloadW, downloadReq)

	if downloadW.Code != http.StatusOK {
		t.Fatalf("expected download status 200, got %d: %s", downloadW.Code, downloadW.Body.String())
	}
	if got := downloadW.Header().Get("Content-Type"); got != "application/pdf" {
		t.Fatalf("expected content type application/pdf, got %q", got)
	}
	if got := downloadW.Header().Get("Content-Disposition"); got != `attachment; filename="report.pdf"` {
		t.Fatalf("expected original filename disposition, got %q", got)
	}
	if got := downloadW.Header().Get("X-Content-SHA256"); got != expectedHash {
		t.Fatalf("expected X-Content-SHA256 %q, got %q", expectedHash, got)
	}
	if got := downloadW.Body.String(); got != "pdf content" {
		t.Fatalf("expected downloaded content, got %q", got)
	}
}

func TestDownloadFileRouteSupportsRangeRequests(t *testing.T) {
	if err := auth.Init("router-download-range-test-secret"); err != nil {
		t.Fatalf("auth.Init returned error: %v", err)
	}

	db := setupRouterTestDB(t)
	service.Init(repository.NewRepository(db))
	service.InitFileStorage(storage.NewLocalStorage(t.TempDir(), "/uploads"))
	r := New()

	register(t, r, "range-download@example.com", "password123")
	token := login(t, r, "range-download@example.com", "password123")

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fileHeader := textproto.MIMEHeader{}
	fileHeader.Set("Content-Disposition", `form-data; name="file"; filename="range.txt"`)
	fileHeader.Set("Content-Type", "text/plain")
	part, err := writer.CreatePart(fileHeader)
	if err != nil {
		t.Fatalf("CreatePart returned error: %v", err)
	}
	if _, err := part.Write([]byte("0123456789")); err != nil {
		t.Fatalf("failed to write multipart file: %v", err)
	}
	if err := writer.WriteField("purpose", string(dto.FilePurposeChatFile)); err != nil {
		t.Fatalf("WriteField returned error: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	uploadReq := httptest.NewRequest(http.MethodPost, "/v1/file/upload", &body)
	uploadReq.Header.Set("Content-Type", writer.FormDataContentType())
	uploadReq.Header.Set("Authorization", "Bearer "+token)
	uploadW := httptest.NewRecorder()
	r.ServeHTTP(uploadW, uploadReq)
	if uploadW.Code != http.StatusOK {
		t.Fatalf("expected upload status 200, got %d: %s", uploadW.Code, uploadW.Body.String())
	}

	var uploadResponse struct {
		Data struct {
			ID     uint   `json:"id"`
			SHA256 string `json:"sha256"`
		} `json:"data"`
	}
	if err := json.Unmarshal(uploadW.Body.Bytes(), &uploadResponse); err != nil {
		t.Fatalf("failed to decode upload response: %v", err)
	}
	expectedHash := sha256String("0123456789")
	if uploadResponse.Data.SHA256 != expectedHash {
		t.Fatalf("expected upload sha256 %q, got %q", expectedHash, uploadResponse.Data.SHA256)
	}

	downloadReq := httptest.NewRequest(http.MethodGet, "/v1/file/"+strconv.FormatUint(uint64(uploadResponse.Data.ID), 10)+"/download", nil)
	downloadReq.Header.Set("Authorization", "Bearer "+token)
	downloadReq.Header.Set("Range", "bytes=2-5")
	downloadW := httptest.NewRecorder()

	r.ServeHTTP(downloadW, downloadReq)

	if downloadW.Code != http.StatusPartialContent {
		t.Fatalf("expected download status 206, got %d: %s", downloadW.Code, downloadW.Body.String())
	}
	if got := downloadW.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("expected Accept-Ranges bytes, got %q", got)
	}
	if got := downloadW.Header().Get("Content-Range"); got != "bytes 2-5/10" {
		t.Fatalf("expected Content-Range bytes 2-5/10, got %q", got)
	}
	if got := downloadW.Header().Get("Content-Length"); got != "4" {
		t.Fatalf("expected Content-Length 4, got %q", got)
	}
	if got := downloadW.Header().Get("X-Content-SHA256"); got != expectedHash {
		t.Fatalf("expected X-Content-SHA256 %q, got %q", expectedHash, got)
	}
	if got := downloadW.Body.String(); got != "2345" {
		t.Fatalf("expected downloaded range content, got %q", got)
	}
}

func TestMultipartUploadRoutesCompleteFile(t *testing.T) {
	if err := auth.Init("router-multipart-upload-test-secret"); err != nil {
		t.Fatalf("auth.Init returned error: %v", err)
	}

	db := setupRouterTestDB(t)
	service.Init(repository.NewRepository(db))
	service.InitFileStorage(storage.NewLocalStorage(t.TempDir(), "/uploads"))
	r := New()

	register(t, r, "multipart@example.com", "password123")
	token := login(t, r, "multipart@example.com", "password123")

	initReq := httptest.NewRequest(http.MethodPost, "/v1/file/upload/init", bytes.NewBufferString(`{
		"filename":"big.txt",
		"size":11,
		"contentType":"text/plain",
		"purpose":"chat_file",
		"chunkSize":6,
		"sha256":"`+sha256String("hello world")+`"
	}`))
	initReq.Header.Set("Content-Type", "application/json")
	initReq.Header.Set("Authorization", "Bearer "+token)
	initW := httptest.NewRecorder()
	r.ServeHTTP(initW, initReq)
	if initW.Code != http.StatusOK {
		t.Fatalf("expected init status 200, got %d: %s", initW.Code, initW.Body.String())
	}

	var initResponse struct {
		Data struct {
			UploadID    string `json:"uploadID"`
			TotalChunks int    `json:"totalChunks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(initW.Body.Bytes(), &initResponse); err != nil {
		t.Fatalf("failed to decode init response: %v", err)
	}
	if initResponse.Data.UploadID == "" || initResponse.Data.TotalChunks != 2 {
		t.Fatalf("unexpected init response: %+v", initResponse.Data)
	}

	uploadChunk := func(index int, content string) {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("chunk", "chunk.part")
		if err != nil {
			t.Fatalf("CreateFormFile returned error: %v", err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatalf("failed to write chunk: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("failed to close multipart writer: %v", err)
		}

		req := httptest.NewRequest(http.MethodPut, "/v1/file/upload/chunks/"+initResponse.Data.UploadID+"/"+strconv.Itoa(index), &body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected chunk status 200, got %d: %s", w.Code, w.Body.String())
		}
	}

	uploadChunk(0, "hello ")
	uploadChunk(1, "world")

	statusReq := httptest.NewRequest(http.MethodGet, "/v1/file/upload/status/"+initResponse.Data.UploadID, nil)
	statusReq.Header.Set("Authorization", "Bearer "+token)
	statusW := httptest.NewRecorder()
	r.ServeHTTP(statusW, statusReq)
	if statusW.Code != http.StatusOK {
		t.Fatalf("expected status route 200, got %d: %s", statusW.Code, statusW.Body.String())
	}
	var statusResponse struct {
		Data struct {
			UploadedChunks []int `json:"uploadedChunks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(statusW.Body.Bytes(), &statusResponse); err != nil {
		t.Fatalf("failed to decode status response: %v", err)
	}
	if len(statusResponse.Data.UploadedChunks) != 2 || statusResponse.Data.UploadedChunks[0] != 0 || statusResponse.Data.UploadedChunks[1] != 1 {
		t.Fatalf("unexpected status response: %+v", statusResponse.Data)
	}

	completeReq := httptest.NewRequest(http.MethodPost, "/v1/file/upload/complete/"+initResponse.Data.UploadID, nil)
	completeReq.Header.Set("Authorization", "Bearer "+token)
	completeW := httptest.NewRecorder()
	r.ServeHTTP(completeW, completeReq)
	if completeW.Code != http.StatusOK {
		t.Fatalf("expected complete status 200, got %d: %s", completeW.Code, completeW.Body.String())
	}
	var completeResponse struct {
		Data struct {
			ID       uint   `json:"id"`
			Filename string `json:"filename"`
			Size     int64  `json:"size"`
			SHA256   string `json:"sha256"`
		} `json:"data"`
	}
	if err := json.Unmarshal(completeW.Body.Bytes(), &completeResponse); err != nil {
		t.Fatalf("failed to decode complete response: %v", err)
	}
	if completeResponse.Data.ID == 0 || completeResponse.Data.Filename != "big.txt" || completeResponse.Data.Size != 11 {
		t.Fatalf("unexpected complete response: %+v", completeResponse.Data)
	}
	if completeResponse.Data.SHA256 != sha256String("hello world") {
		t.Fatalf("expected sha256 %q, got %q", sha256String("hello world"), completeResponse.Data.SHA256)
	}
}

func TestRegisterRejectsMissingRequiredFields(t *testing.T) {
	if err := auth.Init("router-validation-test-secret"); err != nil {
		t.Fatalf("auth.Init returned error: %v", err)
	}

	db := setupRouterTestDB(t)
	service.Init(repository.NewRepository(db))
	r := New()

	req := httptest.NewRequest(http.MethodPost, "/v1/user/register", bytes.NewBufferString(`{"email":"bad@example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request, got %d: %s", w.Code, w.Body.String())
	}

	var response struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected response code 400, got %d", response.Code)
	}
}

func TestGroupRoutesUseAuthenticatedUser(t *testing.T) {
	if err := auth.Init("router-group-test-secret"); err != nil {
		t.Fatalf("auth.Init returned error: %v", err)
	}

	db := setupRouterTestDB(t)
	service.Init(repository.NewRepository(db))
	r := New()

	register(t, r, "owner@example.com", "password123")
	token := login(t, r, "owner@example.com", "password123")

	body := bytes.NewBufferString(`{"name":"team"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/group/create", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected create group status 200, got %d: %s", w.Code, w.Body.String())
	}

	var group model.Group
	if err := db.Where("name = ?", "team").First(&group).Error; err != nil {
		t.Fatalf("failed to query group: %v", err)
	}

	updateBody := bytes.NewBufferString(`{"groupID":` + uintString(group.ID) + `,"name":"renamed"}`)
	updateReq := httptest.NewRequest(http.MethodPost, "/v1/group/update", updateBody)
	updateReq.Header.Set("Content-Type", "application/json")
	updateReq.Header.Set("Authorization", "Bearer "+token)
	updateW := httptest.NewRecorder()

	r.ServeHTTP(updateW, updateReq)

	if updateW.Code != http.StatusOK {
		t.Fatalf("expected update group status 200, got %d: %s", updateW.Code, updateW.Body.String())
	}

	var updated model.Group
	if err := db.First(&updated, group.ID).Error; err != nil {
		t.Fatalf("failed to query updated group: %v", err)
	}
	if updated.Name != "renamed" {
		t.Fatalf("expected group name renamed, got %q", updated.Name)
	}
}

func TestGroupRequestListRoutes(t *testing.T) {
	if err := auth.Init("router-group-request-test-secret"); err != nil {
		t.Fatalf("auth.Init returned error: %v", err)
	}

	db := setupRouterTestDB(t)
	service.Init(repository.NewRepository(db))
	r := New()

	register(t, r, "owner-list@example.com", "password123")
	register(t, r, "member-list@example.com", "password123")
	ownerToken := login(t, r, "owner-list@example.com", "password123")
	memberToken := login(t, r, "member-list@example.com", "password123")

	createReq := httptest.NewRequest(http.MethodPost, "/v1/group/create", bytes.NewBufferString(`{"name":"requests"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Authorization", "Bearer "+ownerToken)
	createW := httptest.NewRecorder()
	r.ServeHTTP(createW, createReq)
	if createW.Code != http.StatusOK {
		t.Fatalf("expected create group status 200, got %d: %s", createW.Code, createW.Body.String())
	}

	var group model.Group
	if err := db.Where("name = ?", "requests").First(&group).Error; err != nil {
		t.Fatalf("failed to query group: %v", err)
	}

	joinReq := httptest.NewRequest(http.MethodPost, "/v1/group/join", bytes.NewBufferString(`{"groupID":`+uintString(group.ID)+`}`))
	joinReq.Header.Set("Content-Type", "application/json")
	joinReq.Header.Set("Authorization", "Bearer "+memberToken)
	joinW := httptest.NewRecorder()
	r.ServeHTTP(joinW, joinReq)
	if joinW.Code != http.StatusOK {
		t.Fatalf("expected join request status 200, got %d: %s", joinW.Code, joinW.Body.String())
	}

	duplicateJoinReq := httptest.NewRequest(http.MethodPost, "/v1/group/join", bytes.NewBufferString(`{"groupID":`+uintString(group.ID)+`}`))
	duplicateJoinReq.Header.Set("Content-Type", "application/json")
	duplicateJoinReq.Header.Set("Authorization", "Bearer "+memberToken)
	duplicateJoinW := httptest.NewRecorder()
	r.ServeHTTP(duplicateJoinW, duplicateJoinReq)
	if duplicateJoinW.Code != http.StatusConflict {
		t.Fatalf("expected duplicate join request status 409, got %d: %s", duplicateJoinW.Code, duplicateJoinW.Body.String())
	}
	var duplicateJoinResponse struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(duplicateJoinW.Body.Bytes(), &duplicateJoinResponse); err != nil {
		t.Fatalf("failed to decode duplicate join response: %v", err)
	}
	if duplicateJoinResponse.Message != "join request already pending" {
		t.Fatalf("expected duplicate join error, got %q", duplicateJoinResponse.Message)
	}
	var member model.User
	if err := db.Where("email = ?", "member-list@example.com").First(&member).Error; err != nil {
		t.Fatalf("failed to query member: %v", err)
	}
	var joinRequestCount int64
	db.Model(&model.GroupJoinRequest{}).
		Where("group_id = ? AND user_id = ? AND status = ?", group.ID, member.ID, model.GroupJoinRequestStatusPending).
		Count(&joinRequestCount)
	if joinRequestCount != 1 {
		t.Fatalf("expected one pending join request, got %d", joinRequestCount)
	}

	myReq := httptest.NewRequest(http.MethodPost, "/v1/group/join-requests/mine", nil)
	myReq.Header.Set("Authorization", "Bearer "+memberToken)
	myW := httptest.NewRecorder()
	r.ServeHTTP(myW, myReq)
	if myW.Code != http.StatusOK {
		t.Fatalf("expected my join requests status 200, got %d: %s", myW.Code, myW.Body.String())
	}

	var myResponse struct {
		Data []dto.GroupJoinRequestOutput `json:"data"`
	}
	if err := json.Unmarshal(myW.Body.Bytes(), &myResponse); err != nil {
		t.Fatalf("failed to decode my join requests: %v", err)
	}
	if len(myResponse.Data) != 1 || myResponse.Data[0].GroupID != group.ID {
		t.Fatalf("unexpected my join requests: %+v", myResponse.Data)
	}

	groupReq := httptest.NewRequest(http.MethodPost, "/v1/group/join-requests", bytes.NewBufferString(`{"groupID":`+uintString(group.ID)+`}`))
	groupReq.Header.Set("Content-Type", "application/json")
	groupReq.Header.Set("Authorization", "Bearer "+ownerToken)
	groupW := httptest.NewRecorder()
	r.ServeHTTP(groupW, groupReq)
	if groupW.Code != http.StatusOK {
		t.Fatalf("expected group join requests status 200, got %d: %s", groupW.Code, groupW.Body.String())
	}

	var groupResponse struct {
		Data []dto.GroupJoinRequestOutput `json:"data"`
	}
	if err := json.Unmarshal(groupW.Body.Bytes(), &groupResponse); err != nil {
		t.Fatalf("failed to decode group join requests: %v", err)
	}
	if len(groupResponse.Data) != 1 || groupResponse.Data[0].GroupID != group.ID {
		t.Fatalf("unexpected group join requests: %+v", groupResponse.Data)
	}

	reviewableReq := httptest.NewRequest(http.MethodPost, "/v1/group/join-requests/reviewable", nil)
	reviewableReq.Header.Set("Authorization", "Bearer "+ownerToken)
	reviewableW := httptest.NewRecorder()
	r.ServeHTTP(reviewableW, reviewableReq)
	if reviewableW.Code != http.StatusOK {
		t.Fatalf("expected reviewable join requests status 200, got %d: %s", reviewableW.Code, reviewableW.Body.String())
	}

	var reviewableResponse struct {
		Data []dto.GroupJoinRequestOutput `json:"data"`
	}
	if err := json.Unmarshal(reviewableW.Body.Bytes(), &reviewableResponse); err != nil {
		t.Fatalf("failed to decode reviewable join requests: %v", err)
	}
	if len(reviewableResponse.Data) != 1 || reviewableResponse.Data[0].GroupID != group.ID {
		t.Fatalf("unexpected reviewable join requests: %+v", reviewableResponse.Data)
	}
}

func TestWebSocketPersistsAndBroadcastsConversationMessage(t *testing.T) {
	if err := auth.Init("router-ws-test-secret"); err != nil {
		t.Fatalf("auth.Init returned error: %v", err)
	}

	db := setupRouterTestDB(t)
	if err := db.AutoMigrate(&model.Message{}); err != nil {
		t.Fatalf("failed to migrate messages: %v", err)
	}
	service.Init(repository.NewRepository(db))
	r := New()

	register(t, r, "ws-alice@example.com", "password123")
	register(t, r, "ws-bob@example.com", "password123")
	aliceToken := login(t, r, "ws-alice@example.com", "password123")
	bobToken := login(t, r, "ws-bob@example.com", "password123")

	var alice model.User
	if err := db.Where("email = ?", "ws-alice@example.com").First(&alice).Error; err != nil {
		t.Fatalf("failed to query alice: %v", err)
	}
	var bob model.User
	if err := db.Where("email = ?", "ws-bob@example.com").First(&bob).Error; err != nil {
		t.Fatalf("failed to query bob: %v", err)
	}

	addReq := httptest.NewRequest(http.MethodPost, "/v1/friend/add", bytes.NewBufferString(`{"friendEmail":"ws-bob@example.com"}`))
	addReq.Header.Set("Content-Type", "application/json")
	addReq.Header.Set("Authorization", "Bearer "+aliceToken)
	addW := httptest.NewRecorder()
	r.ServeHTTP(addW, addReq)
	if addW.Code != http.StatusOK {
		t.Fatalf("expected add friend status 200, got %d: %s", addW.Code, addW.Body.String())
	}

	acceptReq := httptest.NewRequest(http.MethodPost, "/v1/friend/accept", bytes.NewBufferString(`{"requestID":`+uintString(friendRequestID(t, db, alice.ID, bob.ID))+`}`))
	acceptReq.Header.Set("Content-Type", "application/json")
	acceptReq.Header.Set("Authorization", "Bearer "+bobToken)
	acceptW := httptest.NewRecorder()
	r.ServeHTTP(acceptW, acceptReq)
	if acceptW.Code != http.StatusOK {
		t.Fatalf("expected accept friend status 200, got %d: %s", acceptW.Code, acceptW.Body.String())
	}

	server := httptest.NewServer(r)
	defer server.Close()

	bobConn := dialWS(t, server.URL, bobToken)
	defer bobConn.Close()
	aliceConn := dialWS(t, server.URL, aliceToken)
	defer aliceConn.Close()

	clientMsgID := "local-ws-1"
	if err := aliceConn.WriteJSON(map[string]any{
		"type":        dto.WSMessageTypeMessage,
		"clientMsgID": clientMsgID,
		"targetType":  dto.MessageTargetTypePrivate,
		"targetID":    bob.ID,
		"content":     "hello over ws",
	}); err != nil {
		t.Fatalf("failed to write ws message: %v", err)
	}

	if err := aliceConn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("failed to set alice read deadline: %v", err)
	}
	var ack struct {
		Type dto.WSMessageType `json:"type"`
		Data struct {
			ClientMsgID string `json:"clientMsgID"`
			MessageID   uint   `json:"messageID"`
			CreatedAt   string `json:"createdAt"`
		} `json:"data"`
	}
	if err := aliceConn.ReadJSON(&ack); err != nil {
		t.Fatalf("failed to read ack message: %v", err)
	}
	if ack.Type != dto.WSMessageTypeMessageAck {
		t.Fatalf("expected ack type %q, got %q", dto.WSMessageTypeMessageAck, ack.Type)
	}
	if ack.Data.ClientMsgID != clientMsgID || ack.Data.MessageID == 0 || ack.Data.CreatedAt == "" {
		t.Fatalf("unexpected ack data: %+v", ack.Data)
	}

	if err := bobConn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("failed to set read deadline: %v", err)
	}
	var incoming struct {
		Type dto.WSMessageType `json:"type"`
		Data dto.MessageOutput `json:"data"`
	}
	if err := bobConn.ReadJSON(&incoming); err != nil {
		t.Fatalf("failed to read broadcast message: %v", err)
	}

	if incoming.Type != dto.WSMessageTypeMessage {
		t.Fatalf("expected incoming type %q, got %q", dto.WSMessageTypeMessage, incoming.Type)
	}
	if incoming.Data.ID == 0 {
		t.Fatal("expected persisted message ID")
	}
	if incoming.Data.ID != ack.Data.MessageID {
		t.Fatalf("expected pushed message id %d, got %d", ack.Data.MessageID, incoming.Data.ID)
	}
	if incoming.Data.SenderID != alice.ID {
		t.Fatalf("expected sender %d, got %d", alice.ID, incoming.Data.SenderID)
	}
	if incoming.Data.Content != "hello over ws" {
		t.Fatalf("expected content %q, got %q", "hello over ws", incoming.Data.Content)
	}

	var stored model.Message
	if err := db.First(&stored, incoming.Data.ID).Error; err != nil {
		t.Fatalf("expected message to be persisted: %v", err)
	}
}

func TestGroupJoinRequestPushesToOwnerAndAdmin(t *testing.T) {
	if err := auth.Init("router-group-join-push-test-secret"); err != nil {
		t.Fatalf("auth.Init returned error: %v", err)
	}

	db := setupRouterTestDB(t)
	service.Init(repository.NewRepository(db))
	r := New()

	register(t, r, "push-owner@example.com", "password123")
	register(t, r, "push-admin@example.com", "password123")
	register(t, r, "push-member@example.com", "password123")
	ownerToken := login(t, r, "push-owner@example.com", "password123")
	adminToken := login(t, r, "push-admin@example.com", "password123")
	memberToken := login(t, r, "push-member@example.com", "password123")

	var owner, admin model.User
	if err := db.Where("email = ?", "push-owner@example.com").First(&owner).Error; err != nil {
		t.Fatalf("failed to query owner: %v", err)
	}
	if err := db.Where("email = ?", "push-admin@example.com").First(&admin).Error; err != nil {
		t.Fatalf("failed to query admin: %v", err)
	}

	createReq := httptest.NewRequest(http.MethodPost, "/v1/group/create", bytes.NewBufferString(`{"name":"push-group"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Authorization", "Bearer "+ownerToken)
	createW := httptest.NewRecorder()
	r.ServeHTTP(createW, createReq)
	if createW.Code != http.StatusOK {
		t.Fatalf("expected create group status 200, got %d: %s", createW.Code, createW.Body.String())
	}

	var group model.Group
	if err := db.Where("name = ?", "push-group").First(&group).Error; err != nil {
		t.Fatalf("failed to query group: %v", err)
	}
	if err := service.GroupService.InviteToGroup(httptest.NewRequest(http.MethodGet, "/", nil).Context(), group.ID, admin.ID, owner.ID); err != nil {
		t.Fatalf("failed to invite admin: %v", err)
	}
	if _, err := repository.NewRepository(db).UpdateGroupMemberRole(httptest.NewRequest(http.MethodGet, "/", nil).Context(), group.ID, admin.ID, model.GroupMemberRoleAdmin); err != nil {
		t.Fatalf("failed to promote admin: %v", err)
	}

	server := httptest.NewServer(r)
	defer server.Close()

	ownerConn := dialWS(t, server.URL, ownerToken)
	defer ownerConn.Close()
	adminConn := dialWS(t, server.URL, adminToken)
	defer adminConn.Close()

	joinReq := httptest.NewRequest(http.MethodPost, "/v1/group/join", bytes.NewBufferString(`{"groupID":`+uintString(group.ID)+`}`))
	joinReq.Header.Set("Content-Type", "application/json")
	joinReq.Header.Set("Authorization", "Bearer "+memberToken)
	joinW := httptest.NewRecorder()
	r.ServeHTTP(joinW, joinReq)
	if joinW.Code != http.StatusOK {
		t.Fatalf("expected join status 200, got %d: %s", joinW.Code, joinW.Body.String())
	}

	for name, conn := range map[string]*websocket.Conn{"owner": ownerConn, "admin": adminConn} {
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("failed to set %s read deadline: %v", name, err)
		}
		var incoming struct {
			Type dto.WSMessageType `json:"type"`
			Data struct {
				GroupID uint `json:"groupID"`
				UserID  uint `json:"userID"`
			} `json:"data"`
		}
		if err := conn.ReadJSON(&incoming); err != nil {
			t.Fatalf("failed to read %s group join request push: %v", name, err)
		}
		if incoming.Type != dto.WSMessageTypeGroupJoinRequest {
			t.Fatalf("expected %s push type %q, got %q", name, dto.WSMessageTypeGroupJoinRequest, incoming.Type)
		}
		if incoming.Data.GroupID != group.ID {
			t.Fatalf("expected %s push group %d, got %d", name, group.ID, incoming.Data.GroupID)
		}
	}
}

func TestAddFriendPushesRequestToReceiver(t *testing.T) {
	if err := auth.Init("router-friend-request-push-test-secret"); err != nil {
		t.Fatalf("auth.Init returned error: %v", err)
	}

	db := setupRouterTestDB(t)
	service.Init(repository.NewRepository(db))
	r := New()

	register(t, r, "push-friend-alice@example.com", "password123")
	register(t, r, "push-friend-bob@example.com", "password123")
	aliceToken := login(t, r, "push-friend-alice@example.com", "password123")
	bobToken := login(t, r, "push-friend-bob@example.com", "password123")

	var alice model.User
	if err := db.Where("email = ?", "push-friend-alice@example.com").First(&alice).Error; err != nil {
		t.Fatalf("failed to query alice: %v", err)
	}

	server := httptest.NewServer(r)
	defer server.Close()

	bobConn := dialWS(t, server.URL, bobToken)
	defer bobConn.Close()

	addReq := httptest.NewRequest(http.MethodPost, "/v1/friend/add", bytes.NewBufferString(`{"friendEmail":"push-friend-bob@example.com"}`))
	addReq.Header.Set("Content-Type", "application/json")
	addReq.Header.Set("Authorization", "Bearer "+aliceToken)
	addW := httptest.NewRecorder()
	r.ServeHTTP(addW, addReq)
	if addW.Code != http.StatusOK {
		t.Fatalf("expected add friend status 200, got %d: %s", addW.Code, addW.Body.String())
	}

	if err := bobConn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("failed to set read deadline: %v", err)
	}
	var incoming struct {
		Type dto.WSMessageType `json:"type"`
		Data struct {
			RequestID      uint   `json:"requestID"`
			UserID         uint   `json:"userID"`
			RequesterEmail string `json:"requesterEmail"`
		} `json:"data"`
	}
	if err := bobConn.ReadJSON(&incoming); err != nil {
		t.Fatalf("failed to read friend request push: %v", err)
	}
	if incoming.Type != dto.WSMessageTypeFriendRequest {
		t.Fatalf("expected push type %q, got %q", dto.WSMessageTypeFriendRequest, incoming.Type)
	}
	if incoming.Data.RequestID == 0 {
		t.Fatal("expected pushed request id")
	}
	if incoming.Data.UserID != alice.ID {
		t.Fatalf("expected requester user %d, got %d", alice.ID, incoming.Data.UserID)
	}
	if incoming.Data.RequesterEmail != alice.Email {
		t.Fatalf("expected requester email %q, got %q", alice.Email, incoming.Data.RequesterEmail)
	}
}

func TestMessageCoreHTTPFlow(t *testing.T) {
	if err := auth.Init("router-message-core-test-secret"); err != nil {
		t.Fatalf("auth.Init returned error: %v", err)
	}

	db := setupRouterTestDB(t)
	service.Init(repository.NewRepository(db))
	r := New()

	register(t, r, "core-alice@example.com", "password123")
	register(t, r, "core-bob@example.com", "password123")
	aliceToken := login(t, r, "core-alice@example.com", "password123")
	bobToken := login(t, r, "core-bob@example.com", "password123")

	var bob model.User
	if err := db.Where("email = ?", "core-bob@example.com").First(&bob).Error; err != nil {
		t.Fatalf("failed to query bob: %v", err)
	}
	var alice model.User
	if err := db.Where("email = ?", "core-alice@example.com").First(&alice).Error; err != nil {
		t.Fatalf("failed to query alice: %v", err)
	}

	addReq := httptest.NewRequest(http.MethodPost, "/v1/friend/add", bytes.NewBufferString(`{"friendEmail":"core-bob@example.com"}`))
	addReq.Header.Set("Content-Type", "application/json")
	addReq.Header.Set("Authorization", "Bearer "+aliceToken)
	addW := httptest.NewRecorder()
	r.ServeHTTP(addW, addReq)
	if addW.Code != http.StatusOK {
		t.Fatalf("expected add friend status 200, got %d: %s", addW.Code, addW.Body.String())
	}

	acceptReq := httptest.NewRequest(http.MethodPost, "/v1/friend/accept", bytes.NewBufferString(`{"requestID":`+uintString(friendRequestID(t, db, alice.ID, bob.ID))+`}`))
	acceptReq.Header.Set("Content-Type", "application/json")
	acceptReq.Header.Set("Authorization", "Bearer "+bobToken)
	acceptW := httptest.NewRecorder()
	r.ServeHTTP(acceptW, acceptReq)
	if acceptW.Code != http.StatusOK {
		t.Fatalf("expected accept friend status 200, got %d: %s", acceptW.Code, acceptW.Body.String())
	}

	conversation, err := repository.NewRepository(db).GetPrivateConversationBetweenUsers(
		httptest.NewRequest(http.MethodGet, "/", nil).Context(),
		alice.ID,
		bob.ID,
	)
	if err != nil {
		t.Fatalf("expected accepting friend request to create private conversation: %v", err)
	}

	messages := []model.Message{
		{ConversationID: conversation.ID, SenderID: bob.ID, Content: "oldest"},
		{ConversationID: conversation.ID, SenderID: bob.ID, Content: "middle"},
		{ConversationID: conversation.ID, SenderID: bob.ID, Content: "newest"},
	}
	if err := db.Create(&messages).Error; err != nil {
		t.Fatalf("failed to create messages: %v", err)
	}

	listReq := httptest.NewRequest(http.MethodPost, "/v1/message/list", bytes.NewBufferString(`{"targetType":"`+string(dto.MessageTargetTypePrivate)+`","targetID":`+uintString(bob.ID)+`,"limit":2}`))
	listReq.Header.Set("Content-Type", "application/json")
	listReq.Header.Set("Authorization", "Bearer "+aliceToken)
	listW := httptest.NewRecorder()
	r.ServeHTTP(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("expected message list status 200, got %d: %s", listW.Code, listW.Body.String())
	}

	var listResponse struct {
		Data []dto.MessageOutput `json:"data"`
	}
	if err := json.Unmarshal(listW.Body.Bytes(), &listResponse); err != nil {
		t.Fatalf("failed to decode message list response: %v", err)
	}
	if len(listResponse.Data) != 2 ||
		listResponse.Data[0].Content != "newest" ||
		listResponse.Data[1].Content != "middle" {
		t.Fatalf("unexpected message list: %+v", listResponse.Data)
	}

	beforeMessageID := listResponse.Data[len(listResponse.Data)-1].ID
	olderReq := httptest.NewRequest(http.MethodPost, "/v1/message/list", bytes.NewBufferString(`{"targetType":"`+string(dto.MessageTargetTypePrivate)+`","targetID":`+uintString(bob.ID)+`,"beforeMessageID":`+uintString(beforeMessageID)+`,"limit":2}`))
	olderReq.Header.Set("Content-Type", "application/json")
	olderReq.Header.Set("Authorization", "Bearer "+aliceToken)
	olderW := httptest.NewRecorder()
	r.ServeHTTP(olderW, olderReq)
	if olderW.Code != http.StatusOK {
		t.Fatalf("expected older message list status 200, got %d: %s", olderW.Code, olderW.Body.String())
	}

	var olderResponse struct {
		Data []dto.MessageOutput `json:"data"`
	}
	if err := json.Unmarshal(olderW.Body.Bytes(), &olderResponse); err != nil {
		t.Fatalf("failed to decode older message list response: %v", err)
	}
	if len(olderResponse.Data) != 1 || olderResponse.Data[0].Content != "oldest" {
		t.Fatalf("unexpected older message list: %+v", olderResponse.Data)
	}

	readReq := httptest.NewRequest(http.MethodPost, "/v1/message/read", bytes.NewBufferString(`{"targetType":"`+string(dto.MessageTargetTypePrivate)+`","targetID":`+uintString(bob.ID)+`,"messageID":`+uintString(messages[2].ID)+`}`))
	readReq.Header.Set("Content-Type", "application/json")
	readReq.Header.Set("Authorization", "Bearer "+aliceToken)
	readW := httptest.NewRecorder()
	r.ServeHTTP(readW, readReq)
	if readW.Code != http.StatusOK {
		t.Fatalf("expected mark read status 200, got %d: %s", readW.Code, readW.Body.String())
	}

	var member model.ConversationMember
	if err := db.Where("conversation_id = ? AND user_id = ?", conversation.ID, alice.ID).First(&member).Error; err != nil {
		t.Fatalf("failed to query conversation member: %v", err)
	}
	if member.LastReadMessageID != messages[2].ID {
		t.Fatalf("expected last read message %d, got %d", messages[2].ID, member.LastReadMessageID)
	}

	server := httptest.NewServer(r)
	defer server.Close()
	bobConn := dialWS(t, server.URL, bobToken)
	defer bobConn.Close()

	readReceiptReq := httptest.NewRequest(http.MethodPost, "/v1/message/read", bytes.NewBufferString(`{"targetType":"`+string(dto.MessageTargetTypePrivate)+`","targetID":`+uintString(bob.ID)+`,"messageID":`+uintString(messages[2].ID)+`}`))
	readReceiptReq.Header.Set("Content-Type", "application/json")
	readReceiptReq.Header.Set("Authorization", "Bearer "+aliceToken)
	readReceiptW := httptest.NewRecorder()
	r.ServeHTTP(readReceiptW, readReceiptReq)
	if readReceiptW.Code != http.StatusOK {
		t.Fatalf("expected read receipt mark status 200, got %d: %s", readReceiptW.Code, readReceiptW.Body.String())
	}

	if err := bobConn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("failed to set bob read deadline: %v", err)
	}
	var receipt struct {
		Type dto.WSMessageType `json:"type"`
		Data struct {
			TargetType dto.MessageTargetType `json:"targetType"`
			TargetID   uint                  `json:"targetID"`
			MessageID  uint                  `json:"messageID"`
			ReaderID   uint                  `json:"readerID"`
		} `json:"data"`
	}
	if err := bobConn.ReadJSON(&receipt); err != nil {
		t.Fatalf("failed to read message read receipt: %v", err)
	}
	if receipt.Type != dto.WSMessageTypeMessageRead ||
		receipt.Data.TargetType != dto.MessageTargetTypePrivate ||
		receipt.Data.MessageID != messages[2].ID ||
		receipt.Data.ReaderID != alice.ID {
		t.Fatalf("unexpected read receipt: %+v", receipt)
	}

	removeReq := httptest.NewRequest(http.MethodPost, "/v1/friend/remove", bytes.NewBufferString(`{"friendID":`+uintString(bob.ID)+`}`))
	removeReq.Header.Set("Content-Type", "application/json")
	removeReq.Header.Set("Authorization", "Bearer "+aliceToken)
	removeW := httptest.NewRecorder()
	r.ServeHTTP(removeW, removeReq)
	if removeW.Code != http.StatusOK {
		t.Fatalf("expected remove friend status 200, got %d: %s", removeW.Code, removeW.Body.String())
	}

	afterRemoveSessionsReq := httptest.NewRequest(http.MethodPost, "/v1/message/sessions", nil)
	afterRemoveSessionsReq.Header.Set("Authorization", "Bearer "+aliceToken)
	afterRemoveSessionsW := httptest.NewRecorder()
	r.ServeHTTP(afterRemoveSessionsW, afterRemoveSessionsReq)
	if afterRemoveSessionsW.Code != http.StatusOK {
		t.Fatalf("expected sessions after remove status 200, got %d: %s", afterRemoveSessionsW.Code, afterRemoveSessionsW.Body.String())
	}
	var afterRemoveSessions struct {
		Data []dto.MessageSessionOutput `json:"data"`
	}
	if err := json.Unmarshal(afterRemoveSessionsW.Body.Bytes(), &afterRemoveSessions); err != nil {
		t.Fatalf("failed to decode sessions after remove: %v", err)
	}
	for _, session := range afterRemoveSessions.Data {
		if session.TargetType == dto.MessageTargetTypePrivate && session.TargetID == bob.ID {
			t.Fatalf("expected removed friend session hidden, got %+v", afterRemoveSessions.Data)
		}
	}

	afterRemoveListReq := httptest.NewRequest(http.MethodPost, "/v1/message/list", bytes.NewBufferString(`{"targetType":"`+string(dto.MessageTargetTypePrivate)+`","targetID":`+uintString(bob.ID)+`,"limit":2}`))
	afterRemoveListReq.Header.Set("Content-Type", "application/json")
	afterRemoveListReq.Header.Set("Authorization", "Bearer "+aliceToken)
	afterRemoveListW := httptest.NewRecorder()
	r.ServeHTTP(afterRemoveListW, afterRemoveListReq)
	if afterRemoveListW.Code == http.StatusOK {
		t.Fatalf("expected list after remove to fail, got 200: %s", afterRemoveListW.Body.String())
	}
}

func TestMessageHTTPFlowSupportsGroupConversation(t *testing.T) {
	if err := auth.Init("router-message-group-test-secret"); err != nil {
		t.Fatalf("auth.Init returned error: %v", err)
	}

	db := setupRouterTestDB(t)
	service.Init(repository.NewRepository(db))
	r := New()

	register(t, r, "group-owner-msg@example.com", "password123")
	ownerToken := login(t, r, "group-owner-msg@example.com", "password123")

	createGroupReq := httptest.NewRequest(http.MethodPost, "/v1/group/create", bytes.NewBufferString(`{"name":"message-group"}`))
	createGroupReq.Header.Set("Content-Type", "application/json")
	createGroupReq.Header.Set("Authorization", "Bearer "+ownerToken)
	createGroupW := httptest.NewRecorder()
	r.ServeHTTP(createGroupW, createGroupReq)
	if createGroupW.Code != http.StatusOK {
		t.Fatalf("expected create group status 200, got %d: %s", createGroupW.Code, createGroupW.Body.String())
	}

	var owner model.User
	if err := db.Where("email = ?", "group-owner-msg@example.com").First(&owner).Error; err != nil {
		t.Fatalf("failed to query owner: %v", err)
	}
	var group model.Group
	if err := db.Where("name = ?", "message-group").First(&group).Error; err != nil {
		t.Fatalf("failed to query group: %v", err)
	}
	var conversation model.Conversation
	if err := db.Where("type = ? AND group_id = ?", model.ConversationTypeGroup, group.ID).First(&conversation).Error; err != nil {
		t.Fatalf("expected group conversation: %v", err)
	}

	message := model.Message{
		ConversationID: conversation.ID,
		SenderID:       owner.ID,
		Content:        "group history",
	}
	if err := db.Create(&message).Error; err != nil {
		t.Fatalf("failed to create group message: %v", err)
	}

	listReq := httptest.NewRequest(http.MethodPost, "/v1/message/list", bytes.NewBufferString(`{"targetType":"`+string(dto.MessageTargetTypeGroup)+`","targetID":`+uintString(group.ID)+`,"limit":20}`))
	listReq.Header.Set("Content-Type", "application/json")
	listReq.Header.Set("Authorization", "Bearer "+ownerToken)
	listW := httptest.NewRecorder()
	r.ServeHTTP(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("expected group message list status 200, got %d: %s", listW.Code, listW.Body.String())
	}

	var listResponse struct {
		Data []dto.MessageOutput `json:"data"`
	}
	if err := json.Unmarshal(listW.Body.Bytes(), &listResponse); err != nil {
		t.Fatalf("failed to decode group message list: %v", err)
	}
	if len(listResponse.Data) != 1 || listResponse.Data[0].Content != "group history" {
		t.Fatalf("unexpected group message list: %+v", listResponse.Data)
	}

	readReq := httptest.NewRequest(http.MethodPost, "/v1/message/read", bytes.NewBufferString(`{"targetType":"`+string(dto.MessageTargetTypeGroup)+`","targetID":`+uintString(group.ID)+`,"messageID":`+uintString(message.ID)+`}`))
	readReq.Header.Set("Content-Type", "application/json")
	readReq.Header.Set("Authorization", "Bearer "+ownerToken)
	readW := httptest.NewRecorder()
	r.ServeHTTP(readW, readReq)
	if readW.Code != http.StatusOK {
		t.Fatalf("expected group mark read status 200, got %d: %s", readW.Code, readW.Body.String())
	}

	var member model.ConversationMember
	if err := db.Where("conversation_id = ? AND user_id = ?", conversation.ID, owner.ID).First(&member).Error; err != nil {
		t.Fatalf("failed to query group conversation member: %v", err)
	}
	if member.LastReadMessageID != message.ID {
		t.Fatalf("expected group last read message %d, got %d", message.ID, member.LastReadMessageID)
	}
}

func TestMessageSessionsListsPrivateAndGroupTargets(t *testing.T) {
	if err := auth.Init("router-message-sessions-test-secret"); err != nil {
		t.Fatalf("auth.Init returned error: %v", err)
	}

	db := setupRouterTestDB(t)
	service.Init(repository.NewRepository(db))
	r := New()

	register(t, r, "session-alice@example.com", "password123")
	register(t, r, "session-bob@example.com", "password123")
	aliceToken := login(t, r, "session-alice@example.com", "password123")
	bobToken := login(t, r, "session-bob@example.com", "password123")

	var alice model.User
	if err := db.Where("email = ?", "session-alice@example.com").First(&alice).Error; err != nil {
		t.Fatalf("failed to query alice: %v", err)
	}
	var bob model.User
	if err := db.Where("email = ?", "session-bob@example.com").First(&bob).Error; err != nil {
		t.Fatalf("failed to query bob: %v", err)
	}

	addReq := httptest.NewRequest(http.MethodPost, "/v1/friend/add", bytes.NewBufferString(`{"friendEmail":"session-bob@example.com"}`))
	addReq.Header.Set("Content-Type", "application/json")
	addReq.Header.Set("Authorization", "Bearer "+aliceToken)
	addW := httptest.NewRecorder()
	r.ServeHTTP(addW, addReq)
	if addW.Code != http.StatusOK {
		t.Fatalf("expected add friend status 200, got %d: %s", addW.Code, addW.Body.String())
	}

	acceptReq := httptest.NewRequest(http.MethodPost, "/v1/friend/accept", bytes.NewBufferString(`{"requestID":`+uintString(friendRequestID(t, db, alice.ID, bob.ID))+`}`))
	acceptReq.Header.Set("Content-Type", "application/json")
	acceptReq.Header.Set("Authorization", "Bearer "+bobToken)
	acceptW := httptest.NewRecorder()
	r.ServeHTTP(acceptW, acceptReq)
	if acceptW.Code != http.StatusOK {
		t.Fatalf("expected accept friend status 200, got %d: %s", acceptW.Code, acceptW.Body.String())
	}

	privateConversation, err := repository.NewRepository(db).GetPrivateConversationBetweenUsers(
		httptest.NewRequest(http.MethodGet, "/", nil).Context(),
		alice.ID,
		bob.ID,
	)
	if err != nil {
		t.Fatalf("expected private conversation: %v", err)
	}
	privateMessage := model.Message{
		ConversationID: privateConversation.ID,
		SenderID:       bob.ID,
		Content:        "private latest",
	}
	if err := db.Create(&privateMessage).Error; err != nil {
		t.Fatalf("failed to create private message: %v", err)
	}

	createGroupReq := httptest.NewRequest(http.MethodPost, "/v1/group/create", bytes.NewBufferString(`{"name":"session-group"}`))
	createGroupReq.Header.Set("Content-Type", "application/json")
	createGroupReq.Header.Set("Authorization", "Bearer "+aliceToken)
	createGroupW := httptest.NewRecorder()
	r.ServeHTTP(createGroupW, createGroupReq)
	if createGroupW.Code != http.StatusOK {
		t.Fatalf("expected create group status 200, got %d: %s", createGroupW.Code, createGroupW.Body.String())
	}
	var group model.Group
	if err := db.Where("name = ?", "session-group").First(&group).Error; err != nil {
		t.Fatalf("failed to query group: %v", err)
	}
	groupConversation, err := repository.NewRepository(db).GetConversationByGroupID(
		httptest.NewRequest(http.MethodGet, "/", nil).Context(),
		group.ID,
	)
	if err != nil {
		t.Fatalf("expected group conversation: %v", err)
	}
	groupMessage := model.Message{
		ConversationID: groupConversation.ID,
		SenderID:       alice.ID,
		Content:        "group latest",
	}
	if err := db.Create(&groupMessage).Error; err != nil {
		t.Fatalf("failed to create group message: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/message/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+aliceToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected sessions status 200, got %d: %s", w.Code, w.Body.String())
	}

	var response struct {
		Data []dto.MessageSessionOutput `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode sessions response: %v", err)
	}
	if len(response.Data) != 2 {
		t.Fatalf("expected two sessions, got %+v", response.Data)
	}
	if response.Data[0].TargetType != dto.MessageTargetTypeGroup {
		t.Fatalf("expected group session first by latest message time, got %+v", response.Data)
	}

	sessions := map[dto.MessageTargetType]dto.MessageSessionOutput{}
	for _, session := range response.Data {
		sessions[session.TargetType] = session
	}
	privateSession := sessions[dto.MessageTargetTypePrivate]
	if privateSession.TargetID != bob.ID || privateSession.Name != bob.Nickname || privateSession.UnreadCount != 1 {
		t.Fatalf("unexpected private session: %+v", privateSession)
	}
	if privateSession.LastMessage == nil || privateSession.LastMessage.Content != "private latest" {
		t.Fatalf("unexpected private last message: %+v", privateSession.LastMessage)
	}

	groupSession := sessions[dto.MessageTargetTypeGroup]
	if groupSession.TargetID != group.ID || groupSession.Name != group.Name || groupSession.UnreadCount != 0 {
		t.Fatalf("unexpected group session: %+v", groupSession)
	}
	if groupSession.LastMessage == nil || groupSession.LastMessage.Content != "group latest" {
		t.Fatalf("unexpected group last message: %+v", groupSession.LastMessage)
	}
}

func register(t *testing.T, r http.Handler, email, password string) {
	t.Helper()

	body := bytes.NewBufferString(`{"email":"` + email + `","password":"` + password + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/user/register", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected register status 200, got %d: %s", w.Code, w.Body.String())
	}
}

func login(t *testing.T, r http.Handler, email, password string) string {
	t.Helper()

	body := bytes.NewBufferString(`{"email":"` + email + `","password":"` + password + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/user/login", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected login status 200, got %d: %s", w.Code, w.Body.String())
	}

	var response struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode login response: %v", err)
	}
	if response.Data.Token == "" {
		t.Fatal("expected login response token")
	}
	return response.Data.Token
}

func setupRouterTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	if err := db.AutoMigrate(
		&model.User{},
		&model.FriendRelation{},
		&model.Group{},
		&model.GroupMember{},
		&model.Conversation{},
		&model.ConversationMember{},
		&model.Message{},
		&model.File{},
		&model.UploadSession{},
		&model.UploadChunk{},
		&model.GroupJoinRequest{},
	); err != nil {
		t.Fatalf("failed to migrate test db: %v", err)
	}

	return db
}

func uintString(v uint) string {
	return strconv.FormatUint(uint64(v), 10)
}

func friendRequestID(t *testing.T, db *gorm.DB, requesterID, receiverID uint) uint {
	t.Helper()

	var relation model.FriendRelation
	if err := db.Where("user_id = ? AND friend_id = ?", requesterID, receiverID).First(&relation).Error; err != nil {
		t.Fatalf("failed to find friend request: %v", err)
	}
	return relation.ID
}

func dialWS(t *testing.T, serverURL, token string) *websocket.Conn {
	t.Helper()

	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("failed to parse test server url: %v", err)
	}
	u.Scheme = "ws"
	u.Path = "/v1/ws"
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	return conn
}
