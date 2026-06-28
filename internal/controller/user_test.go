package controller

import (
	"bytes"
	"chat_proj/internal/auth"
	"chat_proj/internal/model"
	"chat_proj/internal/repository"
	"chat_proj/internal/service"
	"chat_proj/pkg/logger"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMain(m *testing.M) {
	logger.Logger = zap.NewNop()
	os.Exit(m.Run())
}

func TestLoginReturnsAccessToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if err := auth.Init("controller-test-secret"); err != nil {
		t.Fatalf("auth.Init returned error: %v", err)
	}

	db := setupControllerTestDB(t)
	service.Init(repository.NewRepository(db))

	hashed, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("GenerateFromPassword returned error: %v", err)
	}
	user := &model.User{Email: "login@example.com", Password: string(hashed)}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	body := bytes.NewBufferString(`{"email":"login@example.com","password":"correct-password"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/user/login", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r := gin.New()
	r.POST("/v1/user/login", Login)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var response struct {
		Code int `json:"code"`
		Data struct {
			Token           string `json:"token"`
			ExpireAt        int64  `json:"expire_at"`
			RefreshToken    string `json:"refresh_token"`
			RefreshExpireAt int64  `json:"refresh_expire_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if response.Code != 0 {
		t.Fatalf("expected response code 0, got %d", response.Code)
	}
	if response.Data.Token == "" {
		t.Fatal("expected token in response")
	}
	if response.Data.ExpireAt == 0 {
		t.Fatal("expected expire_at in response")
	}
	if response.Data.RefreshToken == "" {
		t.Fatal("expected refresh_token in response")
	}
	if response.Data.RefreshExpireAt == 0 {
		t.Fatal("expected refresh_expire_at in response")
	}

	claims, err := auth.ValidateToken(response.Data.Token)
	if err != nil {
		t.Fatalf("ValidateToken returned error: %v", err)
	}
	if claims.UserID != user.ID || claims.Username != user.Email {
		t.Fatalf("unexpected claims: %+v", claims)
	}

	refreshClaims, err := auth.ValidateRefreshToken(response.Data.RefreshToken)
	if err != nil {
		t.Fatalf("ValidateRefreshToken returned error: %v", err)
	}
	if refreshClaims.UserID != user.ID || refreshClaims.Username != user.Email {
		t.Fatalf("unexpected refresh claims: %+v", refreshClaims)
	}
}

func TestRefreshTokenReturnsNewAccessToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if err := auth.Init("controller-refresh-test-secret"); err != nil {
		t.Fatalf("auth.Init returned error: %v", err)
	}

	refreshToken, _, err := auth.GenerateRefreshToken(42, "refresh@example.com")
	if err != nil {
		t.Fatalf("GenerateRefreshToken returned error: %v", err)
	}

	body := bytes.NewBufferString(`{"refreshToken":"` + refreshToken + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/user/refresh", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r := gin.New()
	r.POST("/v1/user/refresh", RefreshToken)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var response struct {
		Code int `json:"code"`
		Data struct {
			Token           string `json:"token"`
			ExpireAt        int64  `json:"expire_at"`
			RefreshToken    string `json:"refresh_token"`
			RefreshExpireAt int64  `json:"refresh_expire_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if response.Code != 0 || response.Data.Token == "" || response.Data.RefreshToken == "" {
		t.Fatalf("unexpected refresh response: %+v", response)
	}
	claims, err := auth.ValidateToken(response.Data.Token)
	if err != nil {
		t.Fatalf("ValidateToken returned error: %v", err)
	}
	if claims.UserID != 42 || claims.Username != "refresh@example.com" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestRegisterDoesNotReturnUserID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupControllerTestDB(t)
	service.Init(repository.NewRepository(db))

	body := bytes.NewBufferString(`{"email":"new@example.com","password":"password123","nickname":"New User"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/user/register", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r := gin.New()
	r.POST("/v1/user/register", Register)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var response struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if response.Code != 0 {
		t.Fatalf("expected response code 0, got %d", response.Code)
	}
	if len(response.Data) != 0 {
		t.Fatalf("expected register response to omit data, got %s", string(response.Data))
	}
}

func setupControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("failed to migrate test db: %v", err)
	}

	return db
}
