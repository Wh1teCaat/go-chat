package dto

type AddFriendInput struct {
	FriendEmail string `json:"friendEmail" binding:"required,email"`
}

type FriendTargetInput struct {
	FriendID uint `json:"friendID" binding:"required,gt=0"`
}

type FriendRequestActionInput struct {
	RequestID uint `json:"requestID" binding:"required,gt=0"`
}

type FriendOutput struct {
	UserID   uint   `json:"userID"`
	Nickname string `json:"nickname"`
	Avatar   string `json:"avatar"`
	Status   string `json:"status"`
	Online   bool   `json:"online"`
}

type PendingFriendRequestOutput struct {
	RequestID      uint   `json:"requestID"`
	UserID         uint   `json:"userID"`
	RequesterEmail string `json:"requesterEmail"`
	Nickname       string `json:"nickname"`
	Avatar         string `json:"avatar"`
	Status         string `json:"status"`
	CreatedAt      string `json:"createdAt"`
}
