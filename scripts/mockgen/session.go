// BFF 会话注入器：模拟真实客户端全链路——
// 注册/登录拿 JWT → UploadMedia 拿预签名 URL 直传 MinIO → 轮询资产 ACTIVE
// → CreateDraft（挂资产，首张封面）→ PublishDraft → 轮询帖子 PUBLISHED。
// 只走面客 BFF 接口，fanout/搜索索引/热榜等派生数据由既有 consumer 链路自动产生。
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"connectrpc.com/connect"

	bffv1 "rednote/gen/go/bff/v1"
	"rednote/gen/go/bff/v1/bffv1connect"
	commonv1 "rednote/gen/go/common/v1"
	mediav1 "rednote/gen/go/media/v1"
	postv1 "rednote/gen/go/post/v1"

	"google.golang.org/protobuf/types/known/wrapperspb"
)

// mockPassword 统一 mock 密码（满足 auth 强度校验：8~32 位含大小写字母与数字）
const mockPassword = "MockPassw0rd!2026"

const (
	uploadPollTimeout = 90 * time.Second // 资产 ACTIVE 等待上限（media consumer 异步生成变体）
	uploadPollEvery   = time.Second
	publishTimeout    = 60 * time.Second // 帖子 PUBLISHED 等待上限（post consumer 异步审核）
	publishPollEvery  = 500 * time.Millisecond
	signedPutTimeout  = 300 * time.Second // 预签名 URL 直传超时（大图 + 跨网链路留足余量）
	signedPutAttempts = 2                 // 直传失败重试次数（预签名 URL 未过期前可安全重试，PUT 幂等）
)

type bffSession struct {
	client bffv1connect.BFFServiceClient
	token  string // access token（Bearer 会话）
	email  string
	name   string
	uid    string
	putVia string // 直传直连地址（host:port，绕过 CDN 慢链路；留空走预签名 URL 原链路）
}

// withBearer 为请求注入 Bearer 会话（与 test/e2e 同款）
func withBearer[T any](req *connect.Request[T], token string) *connect.Request[T] {
	req.Header().Set("Authorization", "Bearer "+token)
	return req
}

// bizFail 业务失败统一错误（fail=true 时的可枚举错误）
func bizFail(op string, err *commonv1.Error) error {
	return fmt.Errorf("%s 业务失败: code=%d msg=%s", op, err.GetCode(), err.GetMsg())
}

// ensureSession 登录已有 mock 用户，未注册则先注册；并拉取 uid（互动 Follow 用）
func ensureSession(ctx context.Context, client bffv1connect.BFFServiceClient, us *userState) (*bffSession, error) {
	s := &bffSession{client: client, email: us.Email, name: us.Name}

	loginErr := s.login(ctx)
	if loginErr != nil {
		// 登录失败可能是账号不存在（首次运行）或状态文件丢失，注册兜底后重试登录
		if regErr := s.register(ctx); regErr != nil {
			return nil, fmt.Errorf("建立会话 %s 失败（登录: %v；注册: %w）", us.Email, loginErr, regErr)
		}
		if err := s.login(ctx); err != nil {
			return nil, fmt.Errorf("用户 %s 注册后登录失败: %w", us.Email, err)
		}
	}

	prof, err := s.client.GetMyProfile(ctx, withBearer(connect.NewRequest(&bffv1.GetMyProfileRequest{}), s.token))
	if err != nil {
		return nil, fmt.Errorf("GetMyProfile(%s) RPC: %w", us.Email, err)
	}
	if prof.Msg.GetFail() {
		return nil, bizFail("GetMyProfile("+us.Email+")", prof.Msg.GetErr())
	}
	s.uid = prof.Msg.GetProfile().GetBrief().GetUid()
	if s.uid == "" {
		return nil, fmt.Errorf("GetMyProfile(%s) 未返回 uid", us.Email)
	}
	us.UID = s.uid
	return s, nil
}

// updateBio 写入用户简介（确定性文案，重跑幂等覆盖）
func (s *bffSession) updateBio(ctx context.Context, bio string) error {
	resp, err := s.client.UpdateMyProfile(ctx, withBearer(connect.NewRequest(
		&bffv1.UpdateMyProfileRequest{Bio: wrapperspb.String(bio)}), s.token))
	if err != nil {
		return fmt.Errorf("UpdateMyProfile(%s) RPC: %w", s.email, err)
	}
	if resp.Msg.GetFail() {
		return bizFail("UpdateMyProfile("+s.email+")", resp.Msg.GetErr())
	}
	return nil
}

func (s *bffSession) register(ctx context.Context) error {
	resp, err := s.client.Register(ctx, connect.NewRequest(&bffv1.RegisterRequest{
		Email:    s.email,
		Password: mockPassword,
		Name:     s.name,
	}))
	if err != nil {
		return fmt.Errorf("Register(%s) RPC: %w", s.email, err)
	}
	if resp.Msg.GetFail() {
		return bizFail("Register("+s.email+")", resp.Msg.GetErr())
	}
	return nil
}

func (s *bffSession) login(ctx context.Context) error {
	resp, err := s.client.Login(ctx, connect.NewRequest(&bffv1.LoginRequest{
		Email:    s.email,
		Password: mockPassword,
	}))
	if err != nil {
		return fmt.Errorf("Login(%s) RPC: %w", s.email, err)
	}
	if resp.Msg.GetFail() {
		return bizFail("Login("+s.email+")", resp.Msg.GetErr())
	}
	token := resp.Msg.GetAccessToken().GetValue()
	if token == "" {
		return fmt.Errorf("Login(%s) 未返回 access token", s.email)
	}
	s.token = token
	return nil
}

// uploadImage 全链路上传：UploadMedia → 预签名 URL 直传 MinIO → 轮询资产 ACTIVE
// （meta 携带真实宽高/字节数/SHA256，满足 consumer 的元数据一致性校验）
func (s *bffSession) uploadImage(ctx context.Context, img *cachedImage) (string, error) {
	up, err := s.client.UploadMedia(ctx, withBearer(connect.NewRequest(&bffv1.UploadMediaRequest{
		Meta: &mediav1.MediaMetaData{
			Sha_256Sum: img.SHA256,
			SizeBytes:  img.SizeBytes,
			Format:     img.Format,
			Width:      img.Width,
			Height:     img.Height,
		},
	}), s.token))
	if err != nil {
		return "", fmt.Errorf("UploadMedia(%s) RPC: %w", img.ID, err)
	}
	if up.Msg.GetFail() {
		return "", bizFail(fmt.Sprintf("UploadMedia(%s)", img.ID), up.Msg.GetErr())
	}
	assetID := up.Msg.GetAsset().GetBase().GetId()
	if assetID == "" {
		return "", fmt.Errorf("UploadMedia(%s) 未返回 asset id", img.ID)
	}
	if up.Msg.GetSignedUrl() == "" {
		return "", fmt.Errorf("UploadMedia(%s) 未返回预签名 URL", img.ID)
	}
	if err := putSignedURL(ctx, up.Msg.GetSignedUrl(), s.putVia, img); err != nil {
		return "", err
	}
	if err := s.waitAssetActive(ctx, assetID, img.ID); err != nil {
		return "", err
	}
	return assetID, nil
}

// putSignedURL 客户端直传 MinIO（预签名 URL，不经服务端）；失败在签名有效期内重试一次。
// putVia 非空时绕过 CDN 直连源站 MinIO（SigV4 只对 Host 头签名，
// dial 目标换源站而 Host 头保持签名域名，签名仍有效）。
func putSignedURL(ctx context.Context, signed, putVia string, img *cachedImage) error {
	var lastErr error
	for attempt := 1; attempt <= signedPutAttempts; attempt++ {
		start := time.Now()
		lastErr = putSignedURLOnce(ctx, signed, putVia, img)
		if lastErr == nil {
			fmt.Printf("  直传 %s (%.1fMB) 耗时 %v\n", img.ID, float64(img.SizeBytes)/(1<<20), time.Since(start).Round(time.Millisecond))
			return nil
		}
		fmt.Printf("警告: 直传 %s 第 %d 次失败: %v\n", img.ID, attempt, lastErr)
	}
	return fmt.Errorf("直传 MinIO %s 重试 %d 次仍失败: %w", img.ID, signedPutAttempts, lastErr)
}

// rewritePutURL 绕过 CDN 直连源站：返回改写后的请求 URL 与需保留的签名 Host 头
func rewritePutURL(signed, putVia string) (string, string, error) {
	u, err := url.Parse(signed)
	if err != nil {
		return "", "", fmt.Errorf("解析预签名 URL: %w", err)
	}
	signHost := u.Host // SigV4 签名的 host（保持原值，含端口则原样保留）
	u.Scheme = "http"  // 源站 MinIO 明文端口；签名不覆盖 scheme
	u.Host = putVia
	return u.String(), signHost, nil
}

func putSignedURLOnce(ctx context.Context, signed, putVia string, img *cachedImage) error {
	f, err := os.Open(img.Path)
	if err != nil {
		return fmt.Errorf("打开待上传图片 %s: %w", img.Path, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			fmt.Printf("警告: 关闭图片文件 %s 失败: %v\n", img.Path, cerr)
		}
	}()

	putURL, signHost := signed, ""
	if putVia != "" {
		var err error
		putURL, signHost, err = rewritePutURL(signed, putVia)
		if err != nil {
			return err
		}
	}
	putCtx, cancel := context.WithTimeout(ctx, signedPutTimeout)
	defer cancel()
	// 包一层匿名 Reader：http transport 会自动 Close *os.File 类型的 body，
	// 包装后由本函数 defer 关闭，避免二次关闭告警
	req, err := http.NewRequestWithContext(putCtx, http.MethodPut, putURL, struct{ io.Reader }{f})
	if err != nil {
		return fmt.Errorf("构造直传请求 %s: %w", img.ID, err)
	}
	if signHost != "" {
		req.Host = signHost // Host 头保持签名域名，SigV4 校验才通过
	}
	req.ContentLength = img.SizeBytes
	req.Header.Set("Content-Type", img.MIME)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("直传 MinIO %s: %w", img.ID, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("直传 MinIO %s 返回 %d", img.ID, resp.StatusCode)
	}
	return nil
}

// waitAssetActive 轮询资产状态至 ACTIVE（UPLOADING → PROCESSING → ACTIVE 异步链路）
func (s *bffSession) waitAssetActive(ctx context.Context, assetID, whID string) error {
	deadline := time.Now().Add(uploadPollTimeout)
	for {
		got, err := s.client.GetMediaAssetStatus(ctx, withBearer(connect.NewRequest(
			&bffv1.GetMediaAssetStatusRequest{Ids: []string{assetID}}), s.token))
		if err == nil && !got.Msg.GetFail() && len(got.Msg.GetStatuses()) > 0 && !got.Msg.GetStatuses()[0].GetFail() {
			status := got.Msg.GetStatuses()[0].GetStatus()
			switch status {
			case mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ACTIVE:
				return nil
			case mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_FAILED,
				mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_BANNED,
				mediav1.MediaAssetStatus_MEDIA_ASSET_STATUS_ERROR:
				return fmt.Errorf("资产 %s（wallhaven %s）处理失败，终态 %s", assetID, whID, status)
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("资产 %s（wallhaven %s）等待 ACTIVE 超时（>%v）", assetID, whID, uploadPollTimeout)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待资产 %s ACTIVE 中断: %w", assetID, ctx.Err())
		case <-time.After(uploadPollEvery):
		}
	}
}

// injectPost 单帖全链路注入：上传图片 → 建草稿 → 发布 → 等待 PUBLISHED，进度逐帖落盘
// （失败也记录错误上下文，重跑同一命令自动补齐）
func (s *bffSession) injectPost(ctx context.Context, plan *postPlan, img *cachedImage, st *mockState) (string, error) {
	assetID, err := s.uploadImage(ctx, img)
	if err != nil {
		_ = st.setPost(plan.wp.ID, &postState{Err: err.Error()})
		return "", err
	}
	postID, err := s.createAndPublish(ctx, plan, assetID)
	ps := &postState{AssetID: assetID, PostID: postID, Published: err == nil}
	if err != nil {
		ps.Err = err.Error()
	}
	if serr := st.setPost(plan.wp.ID, ps); serr != nil {
		return postID, fmt.Errorf("帖子 %s 注入成功但进度落盘失败（重跑会重复发帖，请手动处理）: %w", plan.wp.ID, serr)
	}
	return postID, err
}

// createAndPublish 两步发布：CreateDraft（挂资产 + 元数据）→ PublishDraft → 等待 PUBLISHED
// （发布审核由 post consumer 校验媒体 ACTIVE 后自动流转，无需人工过审）
func (s *bffSession) createAndPublish(ctx context.Context, plan *postPlan, assetID string) (string, error) {
	draft, err := s.client.CreateDraft(ctx, withBearer(connect.NewRequest(&bffv1.CreateDraftRequest{
		Title:    plan.title,
		FullText: plan.text,
		Medias: []*bffv1.AssetRef{
			{AssetId: assetID, Type: mediav1.MediaFileType_MEDIA_FILE_TYPE_ORIGIN},
		},
		Metadata: &commonv1.JsonStr{Payload: plan.meta},
	}), s.token))
	if err != nil {
		return "", fmt.Errorf("CreateDraft(%s) RPC: %w", plan.wp.ID, err)
	}
	if draft.Msg.GetFail() {
		return "", bizFail(fmt.Sprintf("CreateDraft(%s)", plan.wp.ID), draft.Msg.GetErr())
	}
	postID := draft.Msg.GetPost().GetPostId()
	if postID == "" {
		return "", fmt.Errorf("CreateDraft(%s) 未返回 post_id", plan.wp.ID)
	}

	pub, err := s.client.PublishDraft(ctx, withBearer(connect.NewRequest(
		&bffv1.PublishDraftRequest{PostId: postID}), s.token))
	if err != nil {
		return postID, fmt.Errorf("PublishDraft(%s) RPC: %w", postID, err)
	}
	if pub.Msg.GetFail() {
		return postID, bizFail(fmt.Sprintf("PublishDraft(%s)", postID), pub.Msg.GetErr())
	}
	if err := s.waitPublished(ctx, postID); err != nil {
		return postID, err
	}
	return postID, nil
}

// waitPublished 轮询帖子状态至 PUBLISHED（作者视角可见 AUDITING 等中间态）
func (s *bffSession) waitPublished(ctx context.Context, postID string) error {
	deadline := time.Now().Add(publishTimeout)
	for {
		got, err := s.client.GetPost(ctx, withBearer(connect.NewRequest(
			&bffv1.GetPostRequest{PostId: postID}), s.token))
		if err == nil && !got.Msg.GetFail() {
			if got.Msg.GetPost().GetStatus() == postv1.PostStatus_POST_STATUS_PUBLISHED {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("帖子 %s 等待 PUBLISHED 超时（>%v）", postID, publishTimeout)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待帖子 %s PUBLISHED 中断: %w", postID, ctx.Err())
		case <-time.After(publishPollEvery):
		}
	}
}
