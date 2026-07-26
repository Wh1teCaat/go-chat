package router

import (
	"chat_proj/internal/config"
	"chat_proj/internal/controller"
	"chat_proj/internal/middleware"
	"chat_proj/internal/ratelimit"

	"github.com/gin-gonic/gin"
)

type Options struct {
	RateLimiter ratelimit.Limiter
	// DisableWS 用于拆分部署的 chat-logic 服务：WebSocket 接入由 gateway 承担，
	// logic 不再暴露 /v1/ws。
	DisableWS bool
}

func New() *gin.Engine {
	return NewWithConfig(&config.Config{
		CORS: config.CORSConfig{
			AllowedOrigins: config.DefaultCORSAllowedOrigins(),
		},
	})
}

func NewWithConfig(cfg *config.Config) *gin.Engine {
	return NewWithConfigAndOptions(cfg, Options{
		RateLimiter: ratelimit.NewMemoryLimiter(),
	})
}

func NewWithConfigAndOptions(cfg *config.Config, opts Options) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.RequestID())
	r.Use(middleware.RequestLogger())
	r.Use(middleware.CORS(cfg.CORS.AllowedOrigins))
	if cfg.RateLimit.Enabled {
		limiter := opts.RateLimiter
		if limiter == nil {
			limiter = ratelimit.NewMemoryLimiter()
		}
		r.Use(middleware.RateLimit(limiter, cfg.RateLimit.Limit(), cfg.RateLimit.Window()))
	}

	// /health 注册在 AuthRequired 之前，负载均衡探活不需要凭证。
	r.GET("/health", controller.Health)

	// /uploads 只作为头像等公开资源入口。
	// 聊天附件不能直接静态暴露，必须走 /v1/file/:id/download 做登录和会话权限校验。
	r.GET("/uploads/*filepath", controller.PublicUploadedFile)

	controller.SetWSAllowedOrigins(cfg.CORS.AllowedOrigins)

	r.Use(middleware.AuthRequired())

	group := r.Group("/v1")
	{
		group.POST("/user/register", controller.Register)
		group.POST("/user/login", controller.Login)
		group.POST("/user/refresh", controller.RefreshToken)
		group.POST("/user/logout", controller.Logout)
		if !opts.DisableWS {
			group.GET("/ws", controller.ConnectWS)
		}
		group.POST("/file/upload", controller.UploadFile)
		group.POST("/file/upload/init", controller.InitMultipartUpload)
		group.PUT("/file/upload/chunks/:uploadID/:index", controller.UploadMultipartChunk)
		group.GET("/file/upload/status/:uploadID", controller.GetMultipartUploadStatus)
		group.POST("/file/upload/complete/:uploadID", controller.CompleteMultipartUpload)
		group.DELETE("/file/upload/cancel/:uploadID", controller.CancelMultipartUpload)
		group.GET("/file/:id/download", controller.DownloadFile)
		group.POST("/message/list", controller.ListMessages)
		group.POST("/message/read", controller.MarkMessageRead)
		group.POST("/message/sessions", controller.ListMessageSessions)
		group.POST("/user/update", controller.UpdateUserInfo)
		group.POST("/friend/add", controller.AddFriend)
		group.POST("/friend/accept", controller.AcceptFriend)
		group.POST("/friend/reject", controller.RejectFriend)
		group.POST("/friend/remove", controller.RemoveFriend)
		group.POST("/friend/list", controller.ListFriends)
		group.POST("/friend/pending", controller.ListPendingFriendRequests)
		group.POST("/group/create", controller.CreateGroup)
		group.POST("/group/update", controller.UpdateGroupInfo)
		group.POST("/group/transfer-owner", controller.TransferGroupOwner)
		group.POST("/group/mine", controller.ListMyGroups)
		group.POST("/group/joined", controller.ListJoinedGroups)
		group.POST("/group/join", controller.RequestJoinGroup)
		group.POST("/group/join-review", controller.ReviewGroupJoinRequest)
		group.POST("/group/join-requests/reviewable", controller.ListReviewableGroupJoinRequests)
		group.POST("/group/join-requests", controller.ListGroupJoinRequests)
		group.POST("/group/join-requests/mine", controller.ListMyGroupJoinRequests)
		group.POST("/group/invite", controller.InviteToGroup)
		group.POST("/group/leave", controller.LeaveGroup)
		group.POST("/group/member/remove", controller.RemoveGroupMember)
		group.POST("/group/member/role", controller.UpdateGroupMemberRole)
		group.POST("/group/member/list", controller.ListGroupMembers)
	}

	return r
}
