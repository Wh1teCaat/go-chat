package model

import (
	"time"

	"gorm.io/gorm"
)

const (
	ConversationTypePrivate uint8 = 0
	ConversationTypeGroup   uint8 = 1

	GroupMemberRoleUser  uint8 = 0
	GroupMemberRoleAdmin uint8 = 1
	GroupMemberRoleOwner uint8 = 2

	FriendRelationStatusPending  = "pending"
	FriendRelationStatusAccepted = "accepted"
	FriendRelationStatusRejected = "rejected"

	GroupJoinRequestStatusPending  = "pending"
	GroupJoinRequestStatusApproved = "approved"
	GroupJoinRequestStatusRejected = "rejected"

	UploadSessionStatusPending   = "pending"
	UploadSessionStatusCompleted = "completed"
	UploadSessionStatusCanceled  = "canceled"
)

type User struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Email     string    `gorm:"size:64;uniqueIndex;not null" json:"email"`
	Password  string    `gorm:"size:255;not null" json:"-"`
	Nickname  string    `gorm:"size:64" json:"nickname"`
	Avatar    string    `gorm:"size:255" json:"avatar"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Group struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:128;not null" json:"name"`
	OwnerID   uint      `json:"owner_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type GroupMember struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	GroupID   uint      `gorm:"uniqueIndex:idx_group_user;not null" json:"group_id"`
	UserID    uint      `gorm:"uniqueIndex:idx_group_user;not null" json:"user_id"`
	Role      uint8     `gorm:"not null;default:0" json:"role"` // 0: member, 1: admin, 2: owner
	CreatedAt time.Time `json:"created_at"`
}

type GroupJoinRequest struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	GroupID    uint       `gorm:"index;not null" json:"group_id"`
	UserID     uint       `gorm:"index;not null" json:"user_id"`
	Status     string     `gorm:"size:32;not null" json:"status"` // pending / approved / rejected
	ReviewedBy *uint      `gorm:"index" json:"reviewed_by,omitempty"`
	ReviewedAt *time.Time `json:"reviewed_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

type Conversation struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Type      uint8     `gorm:"not null" json:"type"` // 0: private, 1: group
	GroupID   *uint     `gorm:"index" json:"group_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ConversationMember struct {
	ID                uint      `gorm:"primaryKey" json:"id"`
	ConversationID    uint      `gorm:"uniqueIndex:idx_conv_user;not null" json:"conversation_id"`
	UserID            uint      `gorm:"uniqueIndex:idx_conv_user;not null" json:"user_id"`
	LastReadMessageID uint      `gorm:"not null;default:0" json:"last_read_message_id"`
	CreatedAt         time.Time `json:"created_at"`
}

type Message struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	ConversationID uint      `gorm:"index;not null" json:"conversation_id"`
	SenderID       uint      `gorm:"index;not null" json:"sender_id"`
	Content        string    `gorm:"type:text;not null" json:"content"`
	CreatedAt      time.Time `json:"created_at"`
}

type File struct {
	ID             uint           `gorm:"primaryKey" json:"id"`
	UserID         uint           `gorm:"index;not null" json:"user_id"`
	ConversationID *uint          `gorm:"index" json:"conversation_id,omitempty"`
	OriginalName   string         `gorm:"size:255;not null" json:"original_name"`
	StoredName     string         `gorm:"size:255;not null" json:"stored_name"`
	StorageKey     string         `gorm:"size:512;uniqueIndex;not null" json:"storage_key"`
	URL            string         `gorm:"size:512;not null" json:"url"`
	ContentType    string         `gorm:"size:128;not null" json:"content_type"`
	Size           int64          `gorm:"not null" json:"size"`
	SHA256         string         `gorm:"size:64;not null;default:''" json:"sha256"`
	Purpose        string         `gorm:"size:32;index;not null" json:"purpose"`
	CreatedAt      time.Time      `json:"created_at"`
	DeletedAt      gorm.DeletedAt `gorm:"index" json:"-"`
}

type UploadSession struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	UploadID     string    `gorm:"size:64;uniqueIndex;not null" json:"upload_id"`
	UserID       uint      `gorm:"index;not null" json:"user_id"`
	OriginalName string    `gorm:"size:255;not null" json:"original_name"`
	ContentType  string    `gorm:"size:128;not null" json:"content_type"`
	Size         int64     `gorm:"not null" json:"size"`
	Purpose      string    `gorm:"size:32;index;not null" json:"purpose"`
	ChunkSize    int64     `gorm:"not null" json:"chunk_size"`
	TotalChunks  int       `gorm:"not null" json:"total_chunks"`
	SHA256       string    `gorm:"size:64;not null;default:''" json:"sha256"`
	Status       string    `gorm:"size:32;index;not null" json:"status"`
	ExpiresAt    time.Time `gorm:"index;not null" json:"expires_at"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type UploadChunk struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UploadID  string    `gorm:"size:64;uniqueIndex:idx_upload_chunk;not null" json:"upload_id"`
	Index     int       `gorm:"uniqueIndex:idx_upload_chunk;not null" json:"index"`
	Size      int64     `gorm:"not null" json:"size"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type FriendRelation struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	UserID     uint      `gorm:"index;not null" json:"user_id"`
	FriendID   uint      `gorm:"index;not null" json:"friend_id"`
	UserLowID  uint      `gorm:"uniqueIndex:idx_friend_pair;not null" json:"user_low_id"`
	UserHighID uint      `gorm:"uniqueIndex:idx_friend_pair;not null" json:"user_high_id"`
	Status     string    `gorm:"size:32;not null" json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (f *FriendRelation) BeforeSave(_ *gorm.DB) error {
	if f.UserID <= f.FriendID {
		f.UserLowID = f.UserID
		f.UserHighID = f.FriendID
		return nil
	}
	f.UserLowID = f.FriendID
	f.UserHighID = f.UserID
	return nil
}
