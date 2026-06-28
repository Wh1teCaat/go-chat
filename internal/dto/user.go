package dto

type LoginUserInput struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=6"`
}

type LoginUserOutput struct {
	UserID uint
	Email  string
}

type RefreshTokenInput struct {
	RefreshToken string `json:"refreshToken" binding:"required"`
}

type RegisterUserInput struct {
	Email    string  `json:"email" binding:"required,email"`
	Password string  `json:"password" binding:"required,min=6"`
	Nickname *string `json:"nickname,omitempty" binding:"omitempty,max=64"`
	Avatar   *string `json:"avatar,omitempty" binding:"omitempty,max=255"`
}

type UpdateUserInput struct {
	Nickname *string `json:"nickname,omitempty" binding:"omitempty,max=64"`
	Avatar   *string `json:"avatar,omitempty" binding:"omitempty,max=255"`
}
