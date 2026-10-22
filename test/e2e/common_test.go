// e2e 黑盒测试共享工具：服务地址解析、可达性探测、业务错误断言、测试数据构造。
// 各服务用例文件（auth / media / post / user）只保留自身链路步骤与专属 helper。
//
// 通用约定：
//   - 目标地址用环境变量覆盖（AUTH_E2E_ADDR / MEDIA_E2E_ADDR / POST_E2E_ADDR / USER_E2E_ADDR），
//     默认指向本机对应端口；
//   - 服务不可达时跳过而非失败，便于本地/CI 按需运行；
//   - go test -short 跳过全部 e2e 用例。
package e2e

import (
	"fmt"
	"net/http"
	"os"
	"testing"

	commonv1 "rednote/gen/go/common/v1"
	mediav1 "rednote/gen/go/media/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eAddr 解析 e2e 目标地址：环境变量优先，否则用默认值
func e2eAddr(envKey, fallback string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	return fallback
}

// mustReachable 探测服务可达性（GET 服务前缀路径），不可达则跳过当前测试
func mustReachable(t *testing.T, addr, servicePath string) {
	t.Helper()

	resp, err := http.Get(addr + servicePath)
	if err != nil {
		t.Skipf("e2e: 服务不可达 (%s)，跳过（恢复条件：部署该服务并可达后重跑）: %v", addr, err)
	}
	_ = resp.Body.Close()
}

// assertBizErr 断言业务失败及错误码
func assertBizErr(t *testing.T, fail bool, err *commonv1.Error, want *commonv1.Error, msg string) {
	t.Helper()
	require.True(t, fail, "%s 应业务失败", msg)
	require.NotNil(t, err, "%s 应返回错误信息", msg)
	assert.Equal(t, want.GetCode(), err.GetCode(), "%s 错误码不符: want=%s got=%s",
		msg, want.GetMsg(), err.GetMsg())
}

// e2eID 生成测试用雪花 ID 字符串（业务服务只做 ID 解析，不校验实体存在）
func e2eID(seed int64) string { return fmt.Sprintf("%d", seed) }

// e2eMediaFile 构造媒体文件（业务服务只存关联不校验存在性，obj_key 非空即可）；
// base.id 为文件雪花 ID，须各不相同
func e2eMediaFile(fileSeed, assetID, objKey string) *mediav1.MediaFile {
	return &mediav1.MediaFile{
		Base:    &commonv1.BaseModel{Id: fileSeed},
		AssetId: assetID,
		Type:    mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN,
		ObjKey:  objKey,
		Bucket:  "rednote",
		Meta:    &mediav1.MediaMetaData{Format: mediav1.MediaFormat_MEDIA_FORMAT_JPG, SizeBytes: 1024, Sha_256Sum: "e2e-sha"},
	}
}
