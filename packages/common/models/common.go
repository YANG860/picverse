package models

import (
	"rednote/packages/common/utils"

	"gorm.io/gorm"
)

// BaseModel 数据库模型基础字段
type BaseModel struct {
	ID        int64 `gorm:"primaryKey;autoIncrement:false;comment:雪花ID" json:"id"`
	CreatedAt int64 `gorm:"autoCreateTime:mili;comment:创建时间" json:"created_at"`
	UpdatedAt int64 `gorm:"autoUpdateTime:mili;comment:更新时间" json:"updated_at"`
}

// BeforeCreate GORM 钩子：创建前自动填充雪花 ID
func (b *BaseModel) BeforeCreate(tx *gorm.DB) error {
	if b.ID == 0 {
		b.ID = utils.GenID().Int64()
	}
	return nil
}
