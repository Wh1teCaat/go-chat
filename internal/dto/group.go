package dto

type CreateGroupInput struct {
	Name string `json:"name" binding:"required,max=128"`
}

type UpdateGroupInput struct {
	GroupID uint    `json:"groupID" binding:"required,gt=0"`
	Name    *string `json:"name,omitempty" binding:"omitempty,max=128"`
}

type GroupMemberActionInput struct {
	GroupID uint `json:"groupID" binding:"required,gt=0"`
	UserID  uint `json:"userID" binding:"required,gt=0"`
}

type UpdateGroupMemberRoleInput struct {
	GroupID uint  `json:"groupID" binding:"required,gt=0"`
	UserID  uint  `json:"userID" binding:"required,gt=0"`
	Role    uint8 `json:"role" binding:"oneof=0 1 2"`
}

type TransferGroupOwnerInput struct {
	GroupID  uint `json:"groupID" binding:"required,gt=0"`
	ToUserID uint `json:"toUserID" binding:"required,gt=0"`
}

type JoinGroupInput struct {
	GroupID uint `json:"groupID" binding:"required,gt=0"`
}

type GroupJoinRequestReviewInput struct {
	RequestID  uint   `json:"requestID" binding:"required,gt=0"`
	Status     string `json:"status" binding:"required,oneof=approved rejected"`
	ReviewerID uint   `json:"reviewerID,omitempty"`
}

type GroupOutput struct {
	ID        uint   `json:"id"`
	Name      string `json:"name"`
	OwnerID   uint   `json:"ownerID"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type GroupMemberOutput struct {
	UserID   uint   `json:"userID"`
	Role     uint8  `json:"role"`
	JoinedAt string `json:"joinedAt"`
}

type GroupJoinRequestOutput struct {
	ID         uint   `json:"id"`
	GroupID    uint   `json:"groupID"`
	UserID     uint   `json:"userID"`
	Status     string `json:"status"`
	ReviewedBy *uint  `json:"reviewedBy,omitempty"`
	ReviewedAt string `json:"reviewedAt,omitempty"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
}
