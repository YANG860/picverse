package auth

import (
	authv1 "rednote/gen/go/auth/v1"
	common "rednote/internal/common/models"
)

type UserCredential struct {
	common.BaseModel
	Email        string `gorm:"uniqueIndex"`
	PasswordHash string

	Role authv1.Role

	RefreshTokenIDWeb    string
	RefreshTokenIDMobile string
}
