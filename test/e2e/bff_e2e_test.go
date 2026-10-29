// BFF 聚合网关端到端测试：注册/登录生命周期 → 上传挂载（引用计数） → 草稿全周期
// → 删帖计数回收 → 可见性判定与列表分页。
//
// 前置条件（全栈部署）：BFF、auth/user/media/post 服务、PostgreSQL、JetStream（含 poller、
// media/post consumer）、MinIO 均已运行；默认头像资产 default-avatar 已预置（未预置时
// 注册类用例跳过并给出恢复条件）。引用计数副作用经 media.GetMediaAssets 内部接口直查
// ref_count（免 DB 连接）；MQ 异步链路（卸载/删帖回收）用轮询等待最终一致。
//
// 运行：
//
//	go test ./test/e2e/... -run TestBFF -count=1          # 默认 http://localhost:8084
//	BFF_E2E_ADDR=http://host:8084 go test ./test/e2e/... -run TestBFF
//	go test -short ./...                                  # -short 跳过
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bffv1 "rednote/gen/go/bff/v1"
	"rednote/gen/go/bff/v1/bffv1connect"
	commonv1 "rednote/gen/go/common/v1"
	mediav1 "rednote/gen/go/media/v1"
	"rednote/gen/go/media/v1/mediav1connect"
	postv1 "rednote/gen/go/post/v1"
	"rednote/gen/go/post/v1/postv1connect"
	userv1 "rednote/gen/go/user/v1"
	"rednote/packages/defaults"
	"rednote/packages/errmsg"

	"google.golang.org/protobuf/types/known/wrapperspb"
)

const defaultBFFAddr = "http://localhost:8084"

func bffAddr() string { return e2eAddr("BFF_E2E_ADDR", defaultBFFAddr) }

// withBearer 为请求注入 Bearer 会话（BFF jwt 拦截器解析后注入 claims）
func withBearer[T any](req *connect.Request[T], token string) *connect.Request[T] {
	req.Header().Set("Authorization", "Bearer "+token)
	return req
}

// newBFFClient 创建 BFF connect 客户端并探测可达性（不可达跳过）
func newBFFClient(t *testing.T) bffv1connect.BFFServiceClient {
	t.Helper()
	addr := bffAddr()
	mustReachable(t, addr, "/bff.v1.BFFService/")
	return bffv1connect.NewBFFServiceClient(http.DefaultClient, addr)
}

// newMediaProbe 创建 media 客户端（ref_count 直查与默认资产探测用），不可达跳过
func newMediaProbe(t *testing.T) mediav1connect.MediaServiceClient {
	t.Helper()
	addr := mediaAddr()
	mustReachable(t, addr, "/media.v1.MediaService/")
	return mediav1connect.NewMediaServiceClient(http.DefaultClient, addr)
}

// e2eBFFEmail 唯一测试邮箱（时间戳保证账号独立，账号留存不清理）
func e2eBFFEmail(tag string) string {
	return fmt.Sprintf("e2e-bff-%s-%d@test.local", tag, time.Now().UnixNano())
}

// requireDefaultAvatar 检查默认头像资产已预置；未预置则跳过（恢复条件：部署/种子 default-avatar）
func requireDefaultAvatar(t *testing.T, media mediav1connect.MediaServiceClient) {
	t.Helper()
	got, err := media.GetMediaAssets(context.Background(),
		connect.NewRequest(&mediav1.GetMediaAssetsRequest{Ids: []string{defaults.AvatarAssetID}}))
	require.NoError(t, err, "GetMediaAssets(default-avatar) RPC")
	require.Len(t, got.Msg.GetResults(), 1)
	if got.Msg.GetResults()[0].GetFail() {
		t.Skipf("e2e: 默认头像资产 %s 未预置（err=%d），跳过注册类用例（恢复条件：预置默认头像资产后重跑）",
			defaults.AvatarAssetID, got.Msg.GetResults()[0].GetErr().GetCode())
	}
}

// registerAndLogin 注册独立账号并登录，返回 (access, refresh)
func registerAndLogin(t *testing.T, client bffv1connect.BFFServiceClient, tag string) (string, string) {
	t.Helper()
	email := e2eBFFEmail(tag)
	ctx := context.Background()

	reg, err := client.Register(ctx, connect.NewRequest(&bffv1.RegisterRequest{
		Email: email, Password: e2ePassword, Name: "e2e-bff-" + tag,
	}))
	require.NoError(t, err, "Register RPC")
	require.False(t, reg.Msg.GetFail(), "注册应成功: %v", reg.Msg.GetErr())

	login, err := client.Login(ctx, connect.NewRequest(&bffv1.LoginRequest{Email: email, Password: e2ePassword}))
	require.NoError(t, err, "Login RPC")
	require.False(t, login.Msg.GetFail(), "登录应成功: %v", login.Msg.GetErr())
	require.NotEmpty(t, login.Msg.GetAccessToken().GetValue(), "access token 非空")
	require.NotEmpty(t, login.Msg.GetRefreshToken().GetValue(), "refresh token 非空")
	return login.Msg.GetAccessToken().GetValue(), login.Msg.GetRefreshToken().GetValue()
}

// genTestPNG 生成合法纯色 PNG（不依赖外部资源，尺寸/字节数可精确构造供 meta 校验）
func genTestPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x * 30), G: uint8(y * 30), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img), "生成测试 PNG")
	return buf.Bytes()
}

// uploadAsset 上传媒体并等待就绪：UploadMedia → PUT 预签名 URL → 轮询 ACTIVE，返回 asset_id
// （meta 携带真实宽高/字节数，满足 consumer 的 ValidateImage 元数据一致性校验）
func uploadAsset(t *testing.T, client bffv1connect.BFFServiceClient, token string) string {
	t.Helper()
	ctx := context.Background()

	body := genTestPNG(t, 32, 32)
	up, err := client.UploadMedia(ctx, withBearer(connect.NewRequest(&bffv1.UploadMediaRequest{
		Meta: &mediav1.MediaMetaData{
			Format:    mediav1.MediaFormat_MEDIA_FORMAT_PNG,
			SizeBytes: int64(len(body)),
			Width:     32,
			Height:    32,
		},
	}), token))
	require.NoError(t, err, "UploadMedia RPC")
	require.False(t, up.Msg.GetFail(), "上传应成功: %v", up.Msg.GetErr())
	require.NotEmpty(t, up.Msg.GetSignedUrl(), "预签名 URL 非空")
	require.NotEmpty(t, up.Msg.GetExpireAt(), "过期时间非空")
	assetID := up.Msg.GetAsset().GetBase().GetId()
	require.NotEmpty(t, assetID, "asset id 非空")

	// PUT 真实图片字节到预签名 URL（MinIO 直传）
	put, err := http.NewRequestWithContext(ctx, http.MethodPut, up.Msg.GetSignedUrl(), bytes.NewReader(body))
	require.NoError(t, err, "构造 PUT 请求")
	put.Header.Set("Content-Type", "image/png")
	putResp, err := http.DefaultClient.Do(put)
	require.NoError(t, err, "PUT 预签名 URL 上传")
	require.True(t, putResp.StatusCode < 300, "PUT 上传应成功，状态码 %d", putResp.StatusCode)
	_ = putResp.Body.Close()

	// 上传处理经异步链路（媒体消费者），轮询面客状态接口等待 ACTIVE
	var status mediav1.MediaAssetStatus
	require.Eventually(t, func() bool {
		got, err := client.GetMediaAssetStatus(ctx, withBearer(connect.NewRequest(&bffv1.GetMediaAssetStatusRequest{Ids: []string{assetID}}), token))
		if err != nil || got.Msg.GetFail() || len(got.Msg.GetStatuses()) == 0 || got.Msg.GetStatuses()[0].GetFail() {
			return false
		}
		status = got.Msg.GetStatuses()[0].GetStatus()
		return status == mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE
	}, 60*time.Second, time.Second, "上传后应经消费链路到达 ACTIVE，最终状态: %s", status)
	return assetID
}

// refCount 直查资产引用计数（副作用断言）
func refCount(t *testing.T, media mediav1connect.MediaServiceClient, assetID string) int64 {
	t.Helper()
	got, err := media.GetMediaAssets(context.Background(),
		connect.NewRequest(&mediav1.GetMediaAssetsRequest{Ids: []string{assetID}}))
	require.NoError(t, err, "GetMediaAssets RPC")
	require.Len(t, got.Msg.GetResults(), 1)
	require.False(t, got.Msg.GetResults()[0].GetFail(), "资产 %s 应存在: %v", assetID, got.Msg.GetResults()[0].GetErr())
	return got.Msg.GetResults()[0].GetAsset().GetRefCount()
}

// awaitRefCount 轮询等待引用计数到达期望值（MQ 异步卸载链路最终一致）；
// 失败时输出完整观测轨迹，便于定位计数漂移来源
func awaitRefCount(t *testing.T, media mediav1connect.MediaServiceClient, assetID string, want int64, msg string) {
	t.Helper()
	var trace []int64
	ok := assert.Eventually(t, func() bool {
		cur := refCount(t, media, assetID)
		trace = append(trace, cur)
		return cur == want
	}, 60*time.Second, time.Second)
	if !ok {
		t.Fatalf("%s: ref_count 期望 %d，观测轨迹 %v", msg, want, trace)
	}
}

// inTTLWindow 断言签名 URL 过期时间落在期望 TTL 的容忍窗口内（±1h，容忍时钟偏移）
func inTTLWindow(t *testing.T, expiresAtMS int64, wantTTL time.Duration, msg string) {
	t.Helper()
	got := time.UnixMilli(expiresAtMS)
	want := time.Now().Add(wantTTL)
	assert.WithinDuration(t, want, got, time.Hour, msg)
}

// TestBFFE2EAuthLifecycle E1/E2 注册语义 + 登录/刷新/登出生命周期（含重复注册、凭据错误、登出失效）
func TestBFFE2EAuthLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: -short 模式跳过依赖外部部署服务的测试")
	}
	client := newBFFClient(t)
	media := newMediaProbe(t)
	requireDefaultAvatar(t, media)
	ctx := context.Background()
	email := e2eBFFEmail("auth")

	// ---- Register：成功 + 全字段契约（成功响应仅 fail/err）----
	reg, err := client.Register(ctx, connect.NewRequest(&bffv1.RegisterRequest{
		Email: email, Password: e2ePassword, Name: "e2e-auth",
	}))
	require.NoError(t, err, "Register RPC")
	require.False(t, reg.Msg.GetFail(), "注册应成功: %v", reg.Msg.GetErr())
	assert.Nil(t, reg.Msg.GetErr(), "成功响应不应携带错误码")

	// ---- 重复注册：DuplicateEmail（回归：能力=开户，唯一约束在 auth）----
	reg2, err := client.Register(ctx, connect.NewRequest(&bffv1.RegisterRequest{
		Email: email, Password: e2ePassword, Name: "e2e-auth-2",
	}))
	require.NoError(t, err, "重复 Register RPC")
	assertBizErr(t, reg2.Msg.GetFail(), reg2.Msg.GetErr(), errmsg.DuplicateEmail, "重复注册")

	// ---- Login 错误路径：密码错误 IncorrectCredential ----
	bad, err := client.Login(ctx, connect.NewRequest(&bffv1.LoginRequest{Email: email, Password: "WrongPassw0rd!"}))
	require.NoError(t, err, "错误密码 Login RPC")
	assertBizErr(t, bad.Msg.GetFail(), bad.Msg.GetErr(), errmsg.IncorrectCredential, "密码错误登录")

	// ---- Login 成功：双 token ----
	login, err := client.Login(ctx, connect.NewRequest(&bffv1.LoginRequest{Email: email, Password: e2ePassword}))
	require.NoError(t, err, "Login RPC")
	require.False(t, login.Msg.GetFail(), "登录应成功: %v", login.Msg.GetErr())
	access := login.Msg.GetAccessToken().GetValue()
	refresh := login.Msg.GetRefreshToken().GetValue()
	require.NotEmpty(t, access, "access token 非空")
	require.NotEmpty(t, refresh, "refresh token 非空")

	// ---- GetMyProfile：全字段 + 默认头像挂载（E2 回归：注册即挂 default-avatar）----
	prof, err := client.GetMyProfile(ctx, withBearer(connect.NewRequest(&bffv1.GetMyProfileRequest{}), access))
	require.NoError(t, err, "GetMyProfile RPC")
	require.False(t, prof.Msg.GetFail(), "获取资料应成功: %v", prof.Msg.GetErr())
	assert.Nil(t, prof.Msg.GetErr())
	p := prof.Msg.GetProfile()
	assert.Equal(t, "e2e-auth", p.GetBrief().GetName(), "昵称回显")
	assert.NotEmpty(t, p.GetBrief().GetUid(), "uid 非空")
	assert.Equal(t, defaults.AvatarAssetID, p.GetBrief().GetAvatar().GetAssetId(),
		"注册统一挂默认头像资产（回归：Register 不再自定义头像）")
	assert.Equal(t, userv1.UserStatus_USER_STATUS_ACTIVE, p.GetStatus(), "新用户为活跃状态")

	// ---- RefreshTokens：换发 access，refresh 回填（轮换或旧值均可无脑覆盖）----
	ref, err := client.RefreshTokens(ctx, withBearer(connect.NewRequest(&bffv1.RefreshTokensRequest{}), refresh))
	require.NoError(t, err, "RefreshTokens RPC")
	require.False(t, ref.Msg.GetFail(), "刷新应成功: %v", ref.Msg.GetErr())
	assert.NotEmpty(t, ref.Msg.GetAccessToken().GetValue(), "换发的 access 非空")
	curRefresh := ref.Msg.GetRefreshToken().GetValue()
	assert.NotEmpty(t, curRefresh, "refresh 回填非空（轮换后新值或未轮换旧值）")

	// ---- Logout → 登出后 refresh 失效 ----
	out, err := client.Logout(ctx, withBearer(connect.NewRequest(&bffv1.LogoutRequest{}), access))
	require.NoError(t, err, "Logout RPC")
	require.False(t, out.Msg.GetFail(), "登出应成功: %v", out.Msg.GetErr())

	after, err := client.RefreshTokens(ctx, withBearer(connect.NewRequest(&bffv1.RefreshTokensRequest{}), curRefresh))
	require.NoError(t, err, "登出后 RefreshTokens RPC")
	assertBizErr(t, after.Msg.GetFail(), after.Msg.GetErr(), errmsg.Unauthorized, "登出后 refresh 应失效")
}

// TestBFFE2EPostLifecycle E3/E4 上传挂载（计数）+ 草稿全周期 + 发布
func TestBFFE2EPostLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: -short 模式跳过依赖外部部署服务的测试")
	}
	client := newBFFClient(t)
	media := newMediaProbe(t)
	requireDefaultAvatar(t, media)
	ctx := context.Background()
	access, _ := registerAndLogin(t, client, "post")

	assetA := uploadAsset(t, client, access)
	assetB := uploadAsset(t, client, access)

	// ---- CreateDraft：两槽位挂载 +1，PostView 全字段回显（私有 TTL）----
	meta := &commonv1.JsonStr{Payload: `{"topic":"e2e"}`}
	draft, err := client.CreateDraft(ctx, withBearer(connect.NewRequest(&bffv1.CreateDraftRequest{
		Title:    "e2e draft",
		FullText: "draft body",
		Medias: []*bffv1.AssetRef{
			{AssetId: assetA, Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_540},
			{AssetId: assetB, Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_216},
		},
		Metadata: meta,
	}), access))
	require.NoError(t, err, "CreateDraft RPC")
	require.False(t, draft.Msg.GetFail(), "建草稿应成功: %v", draft.Msg.GetErr())
	pv := draft.Msg.GetPost()
	assert.Equal(t, "e2e draft", pv.GetTitle())
	assert.Equal(t, "draft body", pv.GetFullText())
	assert.Equal(t, meta.GetPayload(), pv.GetMetadata().GetPayload(), "metadata 透传（按 payload 值比较，避开 proto 内部状态差异）")
	assert.Equal(t, postv1.PostStatus_POST_STATUS_DRAFT, pv.GetStatus(), "新建为草稿")
	require.Len(t, pv.GetMedias(), 2, "两槽位媒体回显")
	assert.Equal(t, assetA, pv.GetMedias()[0].GetAssetId(), "槽位顺序保持")
	assert.Equal(t, assetB, pv.GetMedias()[1].GetAssetId())
	for _, mv := range pv.GetMedias() {
		assert.NotEmpty(t, mv.GetUrl(), "预签名 URL 非空")
		inTTLWindow(t, mv.GetExpiresAt(), 24*time.Hour, "草稿媒体私有 TTL 24h")
	}

	// 挂载同步 +1（每槽位一个引用，同步原子生效）
	assert.Equal(t, int64(1), refCount(t, media, assetA), "A 槽位挂载 +1")
	assert.Equal(t, int64(1), refCount(t, media, assetB), "B 槽位挂载 +1")

	// ---- UpdateDraft：部分更新回显，统计不回写 ----
	upd, err := client.UpdateDraft(ctx, withBearer(connect.NewRequest(&bffv1.UpdateDraftRequest{
		PostId: pv.GetPostId(), Title: wrapperspb.String("e2e draft v2"),
	}), access))
	require.NoError(t, err, "UpdateDraft RPC")
	require.False(t, upd.Msg.GetFail(), "更新应成功: %v", upd.Msg.GetErr())
	assert.Equal(t, "e2e draft v2", upd.Msg.GetPost().GetTitle(), "部分更新回显")
	assert.Equal(t, "draft body", upd.Msg.GetPost().GetFullText(), "未提交字段不变")

	// ---- UpdateDraftMedias：换槽位（A/540+B/216 → A/216）；旧资产 MQ 异步卸载 ----
	medias, err := client.UpdateDraftMedias(ctx, withBearer(connect.NewRequest(&bffv1.UpdateDraftMediasRequest{
		PostId: pv.GetPostId(),
		Medias: []*bffv1.AssetRef{{AssetId: assetA, Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_216}},
	}), access))
	require.NoError(t, err, "UpdateDraftMedias RPC")
	require.False(t, medias.Msg.GetFail(), "换媒体应成功: %v", medias.Msg.GetErr())
	require.Len(t, medias.Msg.GetPost().GetMedias(), 1, "替换后仅新槽位")
	assert.Equal(t, assetA, medias.Msg.GetPost().GetMedias()[0].GetAssetId())

	// 旧槽位（A、B）经 MQ 异步卸载：A 净 1（2-1），B 净 0（1-1）；异步链路用轮询断言，不做瞬间断言
	awaitRefCount(t, media, assetA, 1, "替换后 A 计数对齐")
	awaitRefCount(t, media, assetB, 0, "替换后 B 计数归零")

	// ---- PublishDraft：DRAFT → 审核链路 → PUBLISHED（公开 TTL）----
	pub, err := client.PublishDraft(ctx, withBearer(connect.NewRequest(&bffv1.PublishDraftRequest{
		PostId: pv.GetPostId(),
	}), access))
	require.NoError(t, err, "PublishDraft RPC")
	require.False(t, pub.Msg.GetFail(), "发布应成功: %v", pub.Msg.GetErr())

	var status postv1.PostStatus
	require.Eventually(t, func() bool {
		got, err := client.GetPost(ctx, withBearer(connect.NewRequest(&bffv1.GetPostRequest{PostId: pv.GetPostId()}), access))
		if err != nil || got.Msg.GetFail() {
			return false
		}
		status = got.Msg.GetPost().GetStatus()
		return status == postv1.PostStatus_POST_STATUS_PUBLISHED
	}, 60*time.Second, time.Second, "发布后应经审核链路到达 PUBLISHED，最终状态: %s", status)

	pubView, err := client.GetPost(ctx, withBearer(connect.NewRequest(&bffv1.GetPostRequest{PostId: pv.GetPostId()}), access))
	require.NoError(t, err, "发布后 GetPost RPC")
	require.False(t, pubView.Msg.GetFail())
	assert.Equal(t, int64(1), refCount(t, media, assetA), "发布不改计数")
	if len(pubView.Msg.GetPost().GetMedias()) > 0 {
		inTTLWindow(t, pubView.Msg.GetPost().GetMedias()[0].GetExpiresAt(), 7*24*time.Hour, "已发布媒体公开 TTL 7d")
	}
}

// TestBFFE2EDeletePostRecoversRefs E5 删帖计数回收（回归：ref_count 漏减）+ 重复删帖幂等
func TestBFFE2EDeletePostRecoversRefs(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: -short 模式跳过依赖外部部署服务的测试")
	}
	client := newBFFClient(t)
	media := newMediaProbe(t)
	requireDefaultAvatar(t, media)
	ctx := context.Background()
	access, _ := registerAndLogin(t, client, "del")

	asset := uploadAsset(t, client, access)

	// 同资产两个变体两槽位：ref_count == 2
	draft, err := client.CreateDraft(ctx, withBearer(connect.NewRequest(&bffv1.CreateDraftRequest{
		Title:    "e2e del",
		FullText: "e2e delete body",
		Medias: []*bffv1.AssetRef{
			{AssetId: asset, Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_540},
			{AssetId: asset, Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_216},
		},
	}), access))
	require.NoError(t, err, "CreateDraft RPC")
	require.False(t, draft.Msg.GetFail(), "建草稿应成功: %v", draft.Msg.GetErr())
	postID := draft.Msg.GetPost().GetPostId()
	assert.Equal(t, int64(2), refCount(t, media, asset), "两槽位各 +1")

	// 删除：软删隐藏 + 删帖消息链路逐槽回收（两级异步：删帖消费 → 卸载消费）
	del, err := client.DeletePost(ctx, withBearer(connect.NewRequest(&bffv1.DeletePostRequest{PostId: postID}), access))
	require.NoError(t, err, "DeletePost RPC")
	require.False(t, del.Msg.GetFail(), "删除应成功: %v", del.Msg.GetErr())

	got, err := client.GetPost(ctx, withBearer(connect.NewRequest(&bffv1.GetPostRequest{PostId: postID}), access))
	require.NoError(t, err, "删除后 GetPost RPC")
	assertBizErr(t, got.Msg.GetFail(), got.Msg.GetErr(), errmsg.NotFound, "软删帖子对外隐藏")

	awaitRefCount(t, media, asset, 0, "删帖后按槽位回收计数（删几槽减几槽）")

	// 重复删帖幂等：不重复回收计数
	del2, err := client.DeletePost(ctx, withBearer(connect.NewRequest(&bffv1.DeletePostRequest{PostId: postID}), access))
	require.NoError(t, err, "重复 DeletePost RPC 不崩溃")
	_ = del2
	awaitRefCount(t, media, asset, 0, "重复删帖不重复回收")
}

// TestBFFE2EVisibilityAndLists E6/E7 可见性判定（DRAFT 作者/他人、关注关系、封禁）+ 列表状态语义与分页
func TestBFFE2EVisibilityAndLists(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: -short 模式跳过依赖外部部署服务的测试")
	}
	client := newBFFClient(t)
	media := newMediaProbe(t)
	requireDefaultAvatar(t, media)
	ctx := context.Background()
	authorToken, _ := registerAndLogin(t, client, "author")
	readerToken, _ := registerAndLogin(t, client, "reader")

	// 作者建草稿（含一张图，便于可见性断言）
	asset := uploadAsset(t, client, authorToken)
	draft, err := client.CreateDraft(ctx, withBearer(connect.NewRequest(&bffv1.CreateDraftRequest{
		Title:    "visibility draft",
		FullText: "visibility body",
		Medias:   []*bffv1.AssetRef{{AssetId: asset, Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_540}},
	}), authorToken))
	require.NoError(t, err, "CreateDraft RPC")
	require.False(t, draft.Msg.GetFail(), "建草稿应成功: %v", draft.Msg.GetErr())
	postID := draft.Msg.GetPost().GetPostId()

	// ---- DRAFT 可见性：作者可见，他人 Forbidden ----
	own, err := client.GetPost(ctx, withBearer(connect.NewRequest(&bffv1.GetPostRequest{PostId: postID}), authorToken))
	require.NoError(t, err, "作者 GetPost RPC")
	require.False(t, own.Msg.GetFail(), "作者应可见草稿: %v", own.Msg.GetErr())
	assert.Equal(t, postv1.PostStatus_POST_STATUS_DRAFT, own.Msg.GetPost().GetStatus())

	other, err := client.GetPost(ctx, withBearer(connect.NewRequest(&bffv1.GetPostRequest{PostId: postID}), readerToken))
	require.NoError(t, err, "他人 GetPost RPC")
	assertBizErr(t, other.Msg.GetFail(), other.Msg.GetErr(), errmsg.Forbidden, "他人看草稿")

	// ---- 发布后：他人可见，isFollowing 随关注变化 ----
	pub, err := client.PublishDraft(ctx, withBearer(connect.NewRequest(&bffv1.PublishDraftRequest{PostId: postID}), authorToken))
	require.NoError(t, err, "PublishDraft RPC")
	require.False(t, pub.Msg.GetFail(), "发布应成功: %v", pub.Msg.GetErr())
	var status postv1.PostStatus
	require.Eventually(t, func() bool {
		got, err := client.GetPost(ctx, withBearer(connect.NewRequest(&bffv1.GetPostRequest{PostId: postID}), readerToken))
		if err != nil || got.Msg.GetFail() {
			return false
		}
		status = got.Msg.GetPost().GetStatus()
		return status == postv1.PostStatus_POST_STATUS_PUBLISHED
	}, 60*time.Second, time.Second, "发布后应经审核链路到达 PUBLISHED，最终状态: %s", status)

	seen, err := client.GetPost(ctx, withBearer(connect.NewRequest(&bffv1.GetPostRequest{PostId: postID}), readerToken))
	require.NoError(t, err, "他人 GetPost（已发布）RPC")
	require.False(t, seen.Msg.GetFail(), "已发布他人应可见: %v", seen.Msg.GetErr())
	assert.False(t, seen.Msg.GetPost().GetRelation().GetIsFollowing(), "未关注时 isFollowing=false")
	assert.Equal(t, int64(1), refCount(t, media, asset), "可见性流转不改计数")

	// Follow → 关注态回填
	fol, err := client.Follow(ctx, withBearer(connect.NewRequest(&bffv1.FollowRequest{
		TargetUid: own.Msg.GetPost().GetAuthor().GetUid(),
	}), readerToken))
	require.NoError(t, err, "Follow RPC")
	require.False(t, fol.Msg.GetFail(), "关注应成功: %v", fol.Msg.GetErr())

	seen2, err := client.GetPost(ctx, withBearer(connect.NewRequest(&bffv1.GetPostRequest{PostId: postID}), readerToken))
	require.NoError(t, err, "关注后 GetPost RPC")
	require.False(t, seen2.Msg.GetFail())
	assert.True(t, seen2.Msg.GetPost().GetRelation().GetIsFollowing(), "关注后 isFollowing=true")

	// Unfollow → 取消关注态
	unfol, err := client.Unfollow(ctx, withBearer(connect.NewRequest(&bffv1.UnfollowRequest{
		TargetUid: own.Msg.GetPost().GetAuthor().GetUid(),
	}), readerToken))
	require.NoError(t, err, "Unfollow RPC")
	require.False(t, unfol.Msg.GetFail(), "取关应成功: %v", unfol.Msg.GetErr())

	seen3, err := client.GetPost(ctx, withBearer(connect.NewRequest(&bffv1.GetPostRequest{PostId: postID}), readerToken))
	require.NoError(t, err, "取关后 GetPost RPC")
	require.False(t, seen3.Msg.GetFail())
	assert.False(t, seen3.Msg.GetPost().GetRelation().GetIsFollowing(), "取关后 isFollowing=false")

	// ---- 列表状态语义：GetMyPosts 含草稿混合状态，GetUserPosts 恒 PUBLISHED ----
	_, err = client.CreateDraft(ctx, withBearer(connect.NewRequest(&bffv1.CreateDraftRequest{
		Title:    "visibility draft 2",
		FullText: "visibility body 2",
		Medias:   []*bffv1.AssetRef{{AssetId: asset, Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_540}},
	}), authorToken))
	require.NoError(t, err, "第二份草稿 RPC")

	my, err := client.GetMyPosts(ctx, withBearer(connect.NewRequest(&bffv1.GetMyPostsRequest{
		Cursor: &commonv1.Cursor{Limit: 10},
	}), authorToken))
	require.NoError(t, err, "GetMyPosts RPC")
	require.False(t, my.Msg.GetFail(), "我的列表应成功: %v", my.Msg.GetErr())
	require.NotEmpty(t, my.Msg.GetPosts(), "应含至少已发布的帖子")
	for _, c := range my.Msg.GetPosts() {
		assert.Equal(t, own.Msg.GetPost().GetAuthor().GetUid(), c.GetAuthor().GetUid(), "作者即会话用户")
	}
	sawDraft, sawPublished := false, false
	for _, c := range my.Msg.GetPosts() {
		switch c.GetStatus() {
		case postv1.PostStatus_POST_STATUS_DRAFT:
			sawDraft = true
		case postv1.PostStatus_POST_STATUS_PUBLISHED:
			sawPublished = true
		}
	}
	assert.True(t, sawPublished, "我的列表应含已发布")
	assert.True(t, sawDraft, "我的列表应含草稿（状态随 PostBrief 带出）")

	user, err := client.GetUserPosts(ctx, withBearer(connect.NewRequest(&bffv1.GetUserPostsRequest{
		Uid:    own.Msg.GetPost().GetAuthor().GetUid(),
		Cursor: &commonv1.Cursor{Limit: 10},
	}), readerToken))
	require.NoError(t, err, "GetUserPosts RPC")
	require.False(t, user.Msg.GetFail(), "他人列表应成功: %v", user.Msg.GetErr())
	require.NotEmpty(t, user.Msg.GetPosts(), "他人列表应含已发布帖子")
	for _, c := range user.Msg.GetPosts() {
		assert.Equal(t, postv1.PostStatus_POST_STATUS_PUBLISHED, c.GetStatus(), "他人视角恒 PUBLISHED")
	}

	// ---- BANNED 显式拒绝（管理动作经 post 服务 RPC，BFF 透传 Banned）----
	postClient := postv1connect.NewPostServiceClient(http.DefaultClient, postAddr())
	ban, err := postClient.BanPost(ctx, connect.NewRequest(&postv1.BanPostRequest{PostId: postID}))
	require.NoError(t, err, "BanPost（管理动作）RPC")
	require.False(t, ban.Msg.GetFail(), "封禁应成功: %v", ban.Msg.GetErr())

	banned, err := client.GetPost(ctx, withBearer(connect.NewRequest(&bffv1.GetPostRequest{PostId: postID}), readerToken))
	require.NoError(t, err, "封禁后 GetPost RPC")
	assertBizErr(t, banned.Msg.GetFail(), banned.Msg.GetErr(), errmsg.Banned, "封禁帖子应显式返回 Banned")
}
