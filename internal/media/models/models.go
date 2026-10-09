package media

import (
	"errors"
	mediav1 "rednote/gen/go/media/v1"
	common "rednote/internal/common/models"
	"rednote/packages/utils"

	"github.com/bwmarrin/snowflake"
	"gorm.io/datatypes"
)

// MediaAsset 一个媒体资产
// 表示来自同一个原图的一组变体图片
type MediaAsset struct {
	common.BaseModel

	// 状态
	Status mediav1.MediaAssetStatus `gorm:"type:integer;not null;default:0;comment:状态" json:"status"`

	// 引用计数
	RefCount int64 `gorm:"type:integer;not null;default:0;comment:引用计数" json:"ref_count"`

	// 上传者
	UploaderID int64 `gorm:"type:bigint;not null;index;comment:上传者ID" json:"uploader_id"`
}

// TableName 指定表名
func (MediaAsset) TableName() string {
	return "media_assets"
}

// MediaFile 图片的一个变体
// 宽度1080px 540px 216px 之一的.webp图片，或者原图
// 不可变类型 创建后不会修改 只可删除
type MediaFile struct {
	common.BaseModel

	// 所属媒体资产ID
	// 组成复合索引
	AssetID int64 `gorm:"type:bigint;not null;index:idx_media_files_asset_type,priority:1;comment:所属媒体资产ID" json:"asset_id"`

	// 文件类型
	Type mediav1.MediaFileType `gorm:"type:integer;not null;index:idx_media_files_asset_type,priority:2;comment:文件类型" json:"type"`

	// s3 key
	ObjKey string `gorm:"type:varchar(512);not null;comment:s3对象键" json:"obj_key"`
	Bucket string `gorm:"type:varchar(128);not null;comment:s3存储桶" json:"bucket"`

	// 校验
	Sha256Sum string `gorm:"type:varchar(64);not null;comment:SHA256校验和" json:"sha256_sum"`
	SizeBytes int64  `gorm:"type:bigint;not null;comment:文件大小(字节)" json:"size_bytes"`

	// 格式
	Format mediav1.MediaFormat `gorm:"type:integer;not null;comment:媒体格式" json:"format"`
	Height int64               `gorm:"type:bigint;not null;comment:高度" json:"height"`
	Width  int64               `gorm:"type:bigint;not null;comment:宽度" json:"width"`

	// 元数据
	MediaMetaData datatypes.JSONMap `gorm:"type:jsonb;not null;default:'{}';comment:元数据" json:"meta_data"`
}

// TableName 指定表名
func (MediaFile) TableName() string {
	return "media_files"
}
func (m *MediaAsset) ToProto() *mediav1.MediaAsset {
	return &mediav1.MediaAsset{
		Base: m.BaseModel.ToProto(),

		Status:     m.Status,
		RefCount:   m.RefCount,
		UploaderId: snowflake.ParseInt64(m.UploaderID).String(),
	}
}

func (m *MediaFile) ToProto() *mediav1.MediaFile {
	if m == nil {
		return nil
	}

	mediaMetadata, _ := m.MediaMetaData.MarshalJSON()

	return &mediav1.MediaFile{
		Base: m.BaseModel.ToProto(),

		AssetId: snowflake.ParseInt64(m.AssetID).String(),
		Type:    m.Type,
		ObjKey:  m.ObjKey,
		Bucket:  m.Bucket,

		Meta: &mediav1.MediaMetaData{
			Sha_256Sum: m.Sha256Sum,
			SizeBytes:  m.SizeBytes,
			Format:     m.Format,
			Height:     m.Height,
			Width:      m.Width,

			Metadata: mediaMetadata,
		},
	}
}

func MediaFileFromProto(proto *mediav1.MediaFile) (m MediaFile, err error) {
	if proto == nil {
		return MediaFile{}, errors.New("nil media file proto")
	}

	basemodel, err := common.BaseModelFromProto(proto.Base)
	if err != nil {
		return MediaFile{}, err
	}

	id, err := snowflake.ParseString(proto.AssetId)
	if err != nil {
		return MediaFile{}, err
	}

	m.BaseModel = basemodel
	m.AssetID = id.Int64()

	m.Type = proto.Type
	m.ObjKey = proto.ObjKey
	m.Bucket = proto.Bucket

	meta := proto.GetMeta()

	m.Sha256Sum = meta.GetSha_256Sum()
	m.SizeBytes = meta.GetSizeBytes()
	m.Format = meta.GetFormat()
	m.Height = meta.GetHeight()
	m.Width = meta.GetWidth()

	metadata := meta.GetMetadata()

	m.MediaMetaData, err = utils.UnmarshalJSON(metadata)
	if err != nil {
		return MediaFile{}, err
	}

	return m, nil
}
