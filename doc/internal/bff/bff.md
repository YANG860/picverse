# bff — 客户端聚合网关

面向前端与客户端的唯一入口，聚合 auth / user / media / post 四个下游服务。proto 定义于 `proto/bff/v1/`（`bff_service.proto` + `views.proto` + 分域 `*_requests.proto`）。

## 与内部服务的契约差异

| 维度 | 内部服务 | BFF（面客） |
| --- | --- | --- |
| 身份 | 请求体携带 `uid` | **请求体零 uid**，除 Register/Login 外一律由会话解析（BFF 鉴权拦截器校验 token 后注入下游调用），杜绝伪造他人身份 |
| 视图形状 | 服务的表结构投影 | 按客户端页面组装（`PostCard` / `PostDetail` / `ProfileView` / `Relation`），下游多次调用收敛为一次请求 |
| 错误约定 | `fail` + `common.v1.Error`（可枚举错误码），连接层错误走原始 gRPC 错误 | `fail` + `common.v1.Error`；**连接层错误不外泄**：内部记录日志后返回 `InternalError`（见下文“错误处理”） |
| 分页 | 相同：`common.v1.Cursor`，空页返回空列表 + 空游标 | 相同 |

## 接口（BFFService，proto 现定义 31 rpc）

| 域 | rpc | 下游调用 | 聚合职责 |
| --- | --- | --- | --- |
| 认证 | Register | auth.Register（角色固定 ROLE_USER） | 成功不签发令牌，需另行 Login；能力仅“开户”，头像固定挂默认资产（个性化走 UpdateMyAvatar）；失败回滚账户与头像计数 |
| 认证 | Login | auth.Login | 透传 platform |
| 认证 | RefreshTokens | auth.GetAccessToken（+ 临近过期时 auth.GetRefreshToken） | refresh token 请求体直传；始终换 access，仅剩余寿命 < 1/2 总寿命时才轮换 refresh（响应总是回填当前有效值） |
| 认证 | Logout | auth.Logout | uid 来自会话；全平台登出（web/mobile 的 refresh 登录态同事务失效） |
| 认证 | ChangePassword | auth.ChangePassword | uid 来自会话；校验旧密码换写新密码；成功后全端下线（含当前会话，双平台 refresh 登录态同事务失效），响应不签发令牌，客户端跳登录 |
| 用户 | GetMyProfile | user.GetUserProfile | 透传 |
| 用户 | GetUserProfile | user.GetUserProfile + user.GetFollowingRelation | 组装 `ProfileView`（含关注关系） |
| 用户 | UpdateMyProfile | user.UpdateUserProfile | 部分更新透传 |
| 用户 | UpdateMyAvatar | user.UpdateUserAvatar + media.ModifyMediaAssetRefCount | 旧资产 ID 在 BFF 消化：新资产同步 +1、旧资产经 MQ 异步 -1 |
| 用户 | Follow / Unfollow | user.SetFollow / SetUnFollow | uid 来自会话 |
| 用户 | GetFollowingList | user.GetFollowings | 透传游标 |
| 用户 | GetFollowerList | user.GetFollowers | 透传游标（与 GetFollowingList 同构）|
| 用户 | GetFollowRelation | user.GetFollowingRelation | uid 来自会话 |
| 媒体 | UploadMedia | media.UploadMedia | uploader 即会话用户（会话解析后注入）；客户端持签名 URL 直传对象存储 |
| 媒体 | GetMediaAssetStatus | media.GetMediaAssets | 批量查资产状态（逐项 fail），供前端上传后轮询 UPLOADING→ACTIVE；纯读不改计数 |
| 媒体 | GetMyMediaAssets | media.ListMediaAssetsBefore | 素材库（见待定项 2） |
| 帖子 | CreateDraft | media.GetMediaAssets（归属校验）+ post.CreateDraft + media.ModifyMediaAssetRefCount | 资产须属于会话用户；引用计数 +1 |
| 帖子 | UpdateDraft | post.UpdateDraft | 部分更新透传 |
| 帖子 | UpdateDraftMedias | post.UpdateDraftMedias + media.ModifyMediaAssetRefCount | 下游返回的 `old_asset_ids` 在 BFF 消化：旧资产 -1、新资产 +1 |
| 帖子 | PublishDraft | post.PublishDraft | 两步发布的第二步 |
| 帖子 | DeletePost | post.DeletePost | 软删；资产计数由删帖消息链路回收（consumer 逐槽位发 deref 消息，见待定项 3） |
| 帖子 | GetPost | post.ViewPost + user.GetUserBrief + user.GetFollowingRelation | 组装 `PostDetail`；他人访问埋浏览计数（PostEntityCache 计数器 INCR，PV 近似） |
| 帖子 | GetMyPosts | post.ViewPostByAuthor（viewer=author） | 含草稿/审核中（状态随 `PostBrief` 返回），组装 `PostCard`（封面按状态分级签发）|
| 帖子 | GetUserPosts | post.ViewPostByAuthor（viewer=会话用户） | 仅已发布，组装 `PostCard` |
| 帖子 | LikePost / UnlikePost / FavouritePost / UnfavouritePost | post 同名透传 | uid 来自会话；幂等写（前端可重复动作收敛）；响应带最新计数 + `liked/favourited` 态位（前端同步本地集合）；收藏超容量返回 `FavouriteLimitExceeded` |
| 帖子 | GetMyLikedPostIDs / GetMyFavouritePostIDs | post.ListLikedPostIDs / ListFavouritePostIDs | uid 来自会话；返回 post_ids 供前端预取本地判态（赞态近似、藏态精确），游标透传 |
| 搜索 | SearchPosts | discover.SearchPosts + post.GetPostsByIDs | 关键词命中（综合/最新双排序），按命中序回查组装 `PostCard`，过滤非 PUBLISHED 兑底同步窗口 |
| 信息流 | GetFollowingFeed | user.GetFollowings + post.ViewPostByAuthor（逐作者） | 关注的人的已发布帖子按发布时间归并 |

不暴露的内部接口：`BanPost` / `BanUser` / `BanMedia` / `DeleteUnusedMedia` / `SyncPostStat` / `GetPostsByIDs` / `DeleteUnusedPost` 等管理与内部接口一律不经 BFF 面客。

## 关键逻辑

### 错误处理（面客封装）

BFF 由于面向前端，**不返回连接层 error**：下游连接层错误（网络/超时/内部 panic 等）先内部记录结构化日志（调用目标、uid、原始 err），再按契约返回 `fail + InternalError`，不向客户端泄漏内部细节。
其余按惯例处理：

- **下游业务错误**（`fail` + 可枚举 `err`）：原样透传，客户端按错误码分支处理；
- **会话缺失**：`Unauthorized`；**参数/状态校验**：对应可枚举码（`InvalidID` / `InvalidMedia` / `Forbidden` 等）；
- 单接口特例：`Logout` 也遵循本约定（早期版本上抛原始 gRPC 错误，已统一）。

### 媒体视图渲染（MediaView，存储坐标不面客）

面客媒体形态唯一：`bff.v1.MediaView{asset_id, type, url, expires_at, width, height}`——BFF 返回前**内嵌签发**展示 URL，前端不接触存储坐标，也无自行换 URL 的接口（`GetMediaURLs` 已从面客契约删除）。

- **下游响应保持完整 `MediaFile`**（内部管道：落库/consumer 走 S3 均需坐标），BFF 用 `biz.signMediaViews` 机械映射降维 + 抄尺寸元数据；面客镜像视图 `UserBrief/UserProfile/PostView` 替代下游类型直传。
- **签发权留 media 服务**（持 S3 凭证），BFF 每个含媒体的响应收集引用一次**批量** `SignMediaFileURLs`（公开/私有分批各一次内网调用）。
- **TTL 按公开性分级**：公开内容（头像/封面/已发布帖媒体）7d 上限（未来可切 CDN 直链）；私有内容（草稿配图）24h；**过期后前端重拉原接口**刷新（无独立 URL 刷新接口）。
- **鉴权语义**：预签名 URL 本身是自授权凭证，公开/私有的判定由 BFF 各 RPC 的会话/归属校验承担；未授权者拿不到含 URL 的响应。
- **请求侧**：客户端只传 `AssetRef{asset_id, type}`（`UpdateMyAvatar`/`CreateDraft`/`UpdateDraftMedias`），BFF 查完整文件后回填下游。
- **失败语义**：签发任一项失败整体报 `InternalError`，不返回半残 URL。

### 资产引用计数

下游服务只存媒体关联不校验归属与计数（`post.md` 定稿），计数由 BFF 在挂载/卸载点维护：

- **挂载**（CreateDraft / UpdateDraftMedias 新资产 / UpdateMyAvatar 新头像）：先经 `media.GetMediaAsset` 校验资产属于会话用户且状态可用，再 `ModifyMediaAssetRefCount` +1（原子接口）。
- **卸载**（UpdateDraftMedias 的 `old_asset_ids` / UpdateMyAvatar 的 `old_asset_id`）：**MQ 异步**——发 `deref_media` 消息（media 流），由 media consumer 消费递减（`ModifyMediaAssetRefCount` -1），卸载最终一致（削峰、解耦替换路径）；挂载 +1 仍同步，保证挂载成功即计数准确。旧资产 ID 是下游响应中的中间产物，**不得透传给客户端**。
- 计数调整失败时整体返回失败（挂载同步调用失败 / 卸载消息发送失败均返回错误），不产生“挂载成功但计数漂移”的静默状态。
- **失败回滚**（UpdateMyAvatar）：下游更新失败时回滚挂载计数（对新资产发卸载消息）；卸载消息发送失败不打挂已成功的新头像（重试会错卸新资产），记 ERROR 日志待闲置资产回收兜底。投递语义 AtLeastOnce，重复 -1 靠 MQ 消息 ID 幂等去重 + 闲置资产回收（`ref_count <= 0`）兜底。
- **关系查询降级**（GetUserProfile）：关注关系查无记录（NotFound）按未关注/未被关注展示，不打挂主页聚合；其余业务错误透传。
- 共用 helper：`biz.GetAndRefMediaFile(ctx, assetID, ftype)`（取指定变体文件 + 校验资产 `ACTIVE` + 计数 +1）与
  `biz.DeRefMediaFile(ctx, assetID)`（计数 -1）；归属校验不在 helper 内，由调用方按场景比对 `uploader_id`（注册场景固定挂默认头像资产 `defaults.AvatarAssetID`，天然跳过）。

### 发布流程（两步）

下游 post 服务为「草稿 → 发布」状态机，BFF 透传两步：保存草稿（`CreateDraft` / `UpdateDraft` / `UpdateDraftMedias`）→ `PublishDraft`（DRAFT → AUDITING，异步审核通过后 PUBLISHED 对外可见）。前端可停留在草稿箱状态，无需一次成稿。

### 信息流（仅关注流）

`GetFollowingFeed` 由 BFF 聚合：拉取关注列表 → 逐作者调用 `ViewPostByAuthor`（仅已发布）→ 按发布时间归并游标分页。推荐流由后续推荐服务另立接口，不复用本契约。

### 搜索（SearchPosts）

`SearchPosts` 由 BFF 聚合：`discover.SearchPosts`（关键词 + 综合/最新双排序 + offset 游标）→ **按命中顺序**回查 `post.GetPostsByIDs` 组装 `PostCard`（不保证顺序的下游结果按 post_id 对齐）。同步窗口内已软删/封禁的命中在回查后过滤（仅 PUBLISHED 面客，页内条数可能少于 limit）；整页被过滤按翻页到底处理。

## 依赖

- auth / user / media / post 服务（connectrpc 客户端，`packages/rpc` 统一构造：服务发现传输层 + trace/jwt 拦截器，地址源 `config.services`，见 [rpc](../../packages/rpc/rpc.md)）
- `packages/cache`（浏览计数计数器：GetPost 埋点 IncrPostViewCount，RedisPoller 定时批量写回）

## 状态

`internal/bff/v1/`（proto 现定义 31 rpc 全部实现：auth 5 + user 9 + media 2 + post 14 + search 1；单测 `biz_auth_test.go` / `biz_media_test.go` / `biz_user_test.go` / `biz_post_test.go` / `biz_search_test.go` 覆盖 auth/media/user/post/search 五域）：

- `service.go`：BFFServiceHandler 实现全部 rpc 并委托 biz 层
- `biz.go`：`BFFUsecase` 接口 + `BFFUsecaseImpl`，身份经 `sessionUID`（jwt claims）注入：
  - 已实现：Register（成功不签发令牌，需另行 Login；后续步骤失败回滚账户与头像计数）、Login、RefreshTokens、Logout、ChangePassword（改密后全端下线）、UploadMedia、
    GetMyProfile、GetUserProfile（并行聚合 ProfileView）、UpdateMyProfile、UpdateMyAvatar
    （归属校验 + 引用计数挂载/卸载/回滚）、Follow、Unfollow、GetFollowingList、GetFollowerList、GetFollowRelation、
    CreateDraft / UpdateDraft / UpdateDraftMedias / PublishDraft / DeletePost
    （媒体批量归属校验 + 原子挂载 +1，下游失败回滚；旧资产 MQ 异步卸载）、
    GetPost（并行聚合作者/关系，NotFound 降级未关注）、GetMyPosts（状态随 `PostBrief` 返回，封面分级签发）、
    GetUserPosts（恒 PUBLISHED，公开 TTL）、
    SearchPosts（discover 检索 + 按命中序回查组装，过滤非 PUBLISHED 兑底同步窗口）
  - 待实现：GetFollowingFeed（信息流，暂缓）、GetMyMediaAssets（素材库，依赖待定项 2）
- `repo.go`：`DownstreamRepo` 下游客户端集合

`cmd/bff/main.go` 启动入口已落地（`gen.InitializeBFFServiceHandler` 装配下游客户端 + MQ，
main 挂 trace + `jwt.NewServerInterceptor`（token 存在才注入，认证由业务层判定）+ validate 拦截器；
监听 `config.server.bff`（`:8084`））。待补：redis 会话缓存。

## 待定项

1. **媒体展示 URL**（✅ 已闭环）：面客统一 `MediaView`（内嵌签发，TTL 按公开性分级），存储坐标不面客；`GetMediaURLs` 独立换取接口已从面客契约删除。
2. **素材库过滤**：`media.ListMediaAssetsBefore` 无 uploader 过滤参数，"我的素材库"需下游补按上传者过滤后才能真正生效。
3. **删帖后的资产计数**（✅ 已闭环）：DeletePost 软删事务内发 `DeletePost` 消息 → consumer 物理回收时按被删媒体关联逐槽位同事务写 outbox `deref_media` 消息（media consumer 递减引用计数），删几槽减几槽、重复投递不重复减，必达不漏（见 [consumer](../consumer/consumer.md)）。
4. **粉丝列表**（✅ 已闭环）：下游 `user.GetFollowers` 已补齐（游标分页，与 `GetFollowings` 关系反向对称），BFF `GetFollowerList` 已按透传游标实现（与 `GetFollowingList` 同构）。
5. **默认头像资产预置**：Register 固定挂 `packages/defaults.AvatarAssetID`（`"default-avatar"`）的 216 变体，需运维/种子预置该资产并闭环 `packages/defaults/media.go` 的 TODO；预置前无头像注册会因资产查无而失败（已回滚账户，无残留）。
