package user

import (
	commonv1 "rednote/gen/go/common/v1"
	userv1 "rednote/gen/go/user/v1"
	common "rednote/internal/common/models"
	media "rednote/internal/media/models"

	"github.com/bwmarrin/snowflake"
	"gorm.io/datatypes"
)

// UserProfile 用户资料
// uid 由鉴权服务注册时分配，此处建立资料与统计
type UserProfile struct {
	common.BaseModel

	// 昵称
	Name string `gorm:"type:varchar(64);not null;comment:昵称" json:"name"`

	// 头像媒体文件 嵌入
	Avatar *media.MediaFile `gorm:"embedded;embeddedPrefix:avatar_"`

	// 简介
	Bio string `gorm:"type:varchar(512);not null;default:'';comment:简介" json:"bio"`

	// 统计
	Followers  int64 `gorm:"type:bigint;not null;default:0;comment:粉丝数" json:"followers"`
	Followings int64 `gorm:"type:bigint;not null;default:0;comment:关注数" json:"followings"`

	// 状态
	Status userv1.UserStatus `gorm:"type:integer;not null;default:0;comment:用户状态" json:"status"`

	// 元数据
	MetaData datatypes.JSONMap `gorm:"type:jsonb;not null;default:'{}';comment:元数据" json:"meta_data"`
}

// TableName 指定表名
func (UserProfile) TableName() string {
	return "user_profiles"
}

type UserFollow struct {
	common.BaseModel
	// FollowingID 被关注用户ID
	FollowingID int64 `gorm:"type:bigint;not null;index:idx_user_follows_following_id;uniqueIndex:idx_user_follows_fan_following,priority:2;comment:关注的用户ID" json:"following_id"`
	// Following 被关注用户
	Following *UserProfile `gorm:"foreignKey:FollowingID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
	// FanID 粉丝ID
	FanID int64 `gorm:"type:bigint;not null;uniqueIndex:idx_user_follows_fan_following,priority:1;comment:粉丝ID" json:"fan_id"`
	// Fan 粉丝用户
	Fan *UserProfile `gorm:"foreignKey:FanID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
}

// UserStat 用户统计：统计列子集（轻路径 GetUserStat 的返回）
type UserStat struct {
	// ID 用户ID
	ID int64 `json:"id"`
	// 粉丝数
	Followers int64 `json:"followers"`
	// 关注数
	Followings int64 `json:"followings"`
}

func (s *UserStat) ToProto() *userv1.UserStat {
	if s == nil {
		return nil
	}
	return &userv1.UserStat{Followers: s.Followers, Followings: s.Followings}
}

func (p *UserProfile) ToProto() *userv1.UserProfile {

	metadata, _ := p.MetaData.MarshalJSON()

	return &userv1.UserProfile{
		Brief:    p.ToBrief(),
		Bio:      p.Bio,
		Stat:     &userv1.UserStat{Followers: p.Followers, Followings: p.Followings},
		Status:   p.Status,
		Metadata: &commonv1.JsonStr{Payload: string(metadata)},
	}
}

func (p *UserProfile) ToBrief() *userv1.UserBrief {
	brief := &userv1.UserBrief{
		Uid:  snowflake.ParseInt64(p.ID).String(),
		Name: p.Name,
	}

	// 头像在业务层必填（BFF 兜底默认头像），
	if p.Avatar != nil {
		brief.Avatar = p.Avatar.ToProto()
	}

	return brief
}
