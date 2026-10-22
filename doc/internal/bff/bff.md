# bff — 客户端聚合网关

面向前端与客户端的唯一入口，聚合 auth / user / media / post 四个下游服务。proto 定义于 `proto/bff/v1/`（`bff_service.proto` + `views.proto` + 分域 `*_requests.proto`）。

## 与内部服务的契约差异

| 维度 | 内部服务 | BFF（面客） |
| --- | --- | --- |
| 身份 | 请求体携带 `uid` | **请求体零 uid**，除 Register/Login 外一律由会话解析（BFF 鉴权拦截器校验 token 后注入下游调用），杜绝伪造他人身份 |
| 视图形状 | 服务的表结构投影 | 按客户端页面组装（`PostCard` / `PostDetail` / `ProfileView` / `Relation`），下游多次调用收敛为一次请求 |
| 错误约定 | 相同：`fail` + `common.v1.Error`（可枚举错误码），连接层错误走原始 gRPC 错误 | 相同 |
| 分页 | 相同：`common.v1.Cursor`，空页返回空列表 + 空游标 | 相同 |

## 接口（BFFService，23 rpc）

| 域 | rpc | 下游调用 | 聚合职责 |
| --- | --- | --- | --- |
| 认证 | Register | auth.Register（角色固定 ROLE_USER） | 成功即登录，签发双令牌 |
| 认证 | Login | auth.Login | 透传 platform |
| 认证 | RefreshTokens | auth.GetAccessToken + auth.GetRefreshToken | refresh token 请求体直传；先换 access 再轮换 refresh（轮换即失效旧 token） |
| 认证 | Logout | auth.Logout | uid 来自会话 |
| 用户 | GetMyProfile | user.GetUserProfile | 透传 |
| 用户 | GetUserProfile | user.GetUserProfile + user.GetFollowingRelation | 组装 `ProfileView`（含关注关系） |
| 用户 | UpdateMyProfile | user.UpdateUserProfile | 部分更新透传 |
| 用户 | UpdateMyAvatar | user.UpdateUserAvatar + media.ModifyMediaAssetRefCount | 旧资产 ID 在 BFF 消化：新资产 +1、旧资产 -1 |
| 用户 | Follow / Unfollow | user.SetFollow / SetUnFollow | uid 来自会话 |
| 用户 | GetFollowingList | user.GetFollowings | 透传游标 |
| 用户 | GetFollowRelation | user.GetFollowingRelation | uid 来自会话 |
| 媒体 | CreateMediaUpload | media.UploadMedia | uploader 即会话用户；客户端持签名 URL 直传对象存储 |
| 媒体 | GetMyMediaAssets | media.ListMediaAssetsBefore | 素材库（见待定项 2） |
| 帖子 | CreateDraft | media.GetMediaAssets（归属校验）+ post.CreateDraft + media.ModifyMediaAssetRefCount | 资产须属于会话用户；引用计数 +1 |
| 帖子 | UpdateDraft | post.UpdateDraft | 部分更新透传 |
| 帖子 | UpdateDraftMedias | post.UpdateDraftMedias + media.ModifyMediaAssetRefCount | 下游返回的 `old_asset_ids` 在 BFF 消化：旧资产 -1、新资产 +1 |
| 帖子 | PublishDraft | post.PublishDraft | 两步发布的第二步 |
| 帖子 | DeletePost | post.DeletePost | 软删；资产计数回收见待定项 3 |
| 帖子 | GetPost | post.ViewPost + user.GetUserBrief + user.GetFollowingRelation | 组装 `PostDetail` |
| 帖子 | GetMyPosts | post.ViewPostByAuthor（viewer=author） | 含草稿/审核中，组装 `PostCard` |
| 帖子 | GetUserPosts | post.ViewPostByAuthor（viewer=会话用户） | 仅已发布，组装 `PostCard` |
| 信息流 | GetFollowingFeed | user.GetFollowings + post.ViewPostByAuthor（逐作者） | 关注的人的已发布帖子按发布时间归并 |

不暴露的内部接口：`BanPost` / `BanUser` / `BanMedia` / `DeleteUnusedMedia` / `SyncPostStat` / `GetPostsByIDs` / `DeleteUnusedPost` 等管理与内部接口一律不经 BFF 面客。

## 关键逻辑

### 资产引用计数

下游服务只存媒体关联不校验归属与计数（`post.md` 定稿），计数由 BFF 在挂载/卸载点维护：

- **挂载**（CreateDraft / UpdateDraftMedias 新资产 / UpdateMyAvatar 新头像）：先经 `media.GetMediaAssets` 校验资产属于会话用户且状态可用，再 `ModifyMediaAssetRefCount` +1（原子接口）。
- **卸载**（UpdateDraftMedias 的 `old_asset_ids` / UpdateMyAvatar 的 `old_asset_id`）：`ModifyMediaAssetRefCount` -1。旧资产 ID 是下游响应中的中间产物，**不得透传给客户端**。
- 计数调整失败时整体返回失败（连接层 error），不产生“挂载成功但计数漂移”的静默状态。
- 共用 helper：`biz.GetAndRefMediaFile(ctx, assetID, ftype)`（取指定变体文件 + 校验资产 `ACTIVE` + 计数 +1）与
  `biz.DeRefMediaFile(ctx, assetID)`（计数 -1）；归属校验不在 helper 内，由调用方按场景比对 `uploader_id`（注册场景天然跳过）。

### 发布流程（两步）

下游 post 服务为「草稿 → 发布」状态机，BFF 透传两步：保存草稿（`CreateDraft` / `UpdateDraft` / `UpdateDraftMedias`）→ `PublishDraft`（DRAFT → AUDITING，异步审核通过后 PUBLISHED 对外可见）。前端可停留在草稿箱状态，无需一次成稿。

### 信息流（仅关注流）

`GetFollowingFeed` 由 BFF 聚合：拉取关注列表 → 逐作者调用 `ViewPostByAuthor`（仅已发布）→ 按发布时间归并游标分页。推荐流由后续推荐服务另立接口，不复用本契约。

## 依赖

- auth / user / media / post 服务（connectrpc 客户端，`packages/rpc` 统一构造：服务发现传输层 + trace/jwt 拦截器，地址源 `config.services`，见 [rpc](../../packages/rpc/rpc.md)）
- `packages/redis`（会话/缓存，TODO）

## 状态

脚手架已就绪（`internal/bff/v1/`）：

- `service.go`：BFFServiceHandler 实现 23 rpc 接口，全部委托 biz 层
- `biz.go`：`BFFUsecase` 接口 + `BFFUsecaseImpl`；单下游调用的透传接口已实现
  （Login/RefreshTokens/Logout/GetMyProfile/UpdateMyProfile/Follow/Unfollow/
  GetFollowingList/GetFollowRelation/CreateMediaUpload/UpdateDraft/PublishDraft/DeletePost），
  身份经 `sessionUID`（jwt claims）注入；聚合接口为 TODO 桩（返回 Unimplemented）：
  Register（注册后补签发令牌）、GetUserProfile、UpdateMyAvatar、GetMyMediaAssets、
  CreateDraft、UpdateDraftMedias、GetPost、GetMyPosts、GetUserPosts、GetFollowingFeed
- `repo.go`：`DownstreamRepo` 下游客户端集合

待补的装配工作：`cmd/bff/main.go` 启动入口（`gen.InitializeBFFServiceHandler` 已生成，
挂 `jwt.NewServerInterceptor` + trace 即可）、redis 会话缓存。

## 待定项

1. **媒体展示 URL**：下游 `MediaFile` 只有 `obj_key`/`bucket`，签名 GET RPC 尚未提供。当前视图原样携带 `MediaFile`，展示 URL 策略（CDN 直链或补签名 RPC）定稿后在视图层补 `url` 类字段。
2. **素材库过滤**：`media.ListMediaAssetsBefore` 无 uploader 过滤参数，"我的素材库"需下游补按上传者过滤后才能真正生效。
3. **删帖后的资产计数**：DeletePost 软删后帖子仍持有媒体关联，资产计数回收策略（consumer 链路同步回收）待删帖消息链路完善后定稿。
