package gateway

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"chat_proj/internal/rpc/chatpb"
	"chat_proj/internal/ws"
	"chat_proj/pkg/logger"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestMain(m *testing.M) {
	logger.Logger = zap.NewNop()
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

// fakeChatServer 模拟 chat-logic：content 为 "boom" 时返回权限错误，否则回固定 ACK 数据。
type fakeChatServer struct {
	chatpb.UnimplementedChatServiceServer
}

func (s *fakeChatServer) SendMessage(_ context.Context, req *chatpb.SendMessageRequest) (*chatpb.SendMessageResponse, error) {
	if req.GetContent() == "boom" {
		return nil, status.Error(codes.PermissionDenied, "permission denied")
	}
	return &chatpb.SendMessageResponse{
		MessageId:   42,
		CreatedAt:   "2026-07-26T00:00:00Z",
		ClientMsgId: req.GetClientMsgId(),
	}, nil
}

func dialFakeLogic(t *testing.T) chatpb.ChatServiceClient {
	t.Helper()

	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	chatpb.RegisterChatServiceServer(server, &fakeChatServer{})
	go func() {
		_ = server.Serve(lis)
	}()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial bufconn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return chatpb.NewChatServiceClient(conn)
}

func startGateway(t *testing.T) *websocket.Conn {
	t.Helper()

	gw := New(ws.NewHub(), dialFakeLogic(t), nil)

	r := gin.New()
	// 测试里跳过 JWT 校验，直接注入 user_id，等价于 middleware.AuthRequired 通过后的状态。
	r.GET("/v1/ws", func(c *gin.Context) {
		c.Set("user_id", uint(7))
		gw.ConnectWS(c)
	})
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func readEnvelope(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var payload map[string]any
	if err := conn.ReadJSON(&payload); err != nil {
		t.Fatalf("failed to read ws payload: %v", err)
	}
	return payload
}

func TestGatewayForwardsMessageAndReturnsAck(t *testing.T) {
	conn := startGateway(t)

	input := map[string]any{
		"type": "message", "clientMsgID": "c1",
		"targetType": "private", "targetID": 2, "content": "hi",
	}
	raw, _ := json.Marshal(input)
	if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		t.Fatalf("failed to write message: %v", err)
	}

	payload := readEnvelope(t, conn)
	if payload["type"] != "message_ack" {
		t.Fatalf("expected message_ack, got %v", payload)
	}
	data := payload["data"].(map[string]any)
	if data["messageID"].(float64) != 42 || data["clientMsgID"] != "c1" {
		t.Fatalf("unexpected ack data: %v", data)
	}
}

func TestGatewayMapsGRPCErrorToWSError(t *testing.T) {
	conn := startGateway(t)

	input := map[string]any{
		"type": "message", "clientMsgID": "c2",
		"targetType": "private", "targetID": 2, "content": "boom",
	}
	raw, _ := json.Marshal(input)
	if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		t.Fatalf("failed to write message: %v", err)
	}

	payload := readEnvelope(t, conn)
	if payload["type"] != "error" {
		t.Fatalf("expected error envelope, got %v", payload)
	}
	data := payload["data"].(map[string]any)
	if data["code"] != "permission_denied" || data["status"].(float64) != 403 {
		t.Fatalf("unexpected error data: %v", data)
	}
	if data["clientMsgID"] != "c2" {
		t.Fatalf("expected clientMsgID echoed for failure marking, got %v", data)
	}
}
