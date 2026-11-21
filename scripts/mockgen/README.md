# mockgen — mock 数据生成器

从 [wallhaven.cc API](https://wallhaven.cc/help/api) 抓取真实壁纸，走 BFF 面客全链路灌入
mock 用户 / 帖子 / 互动数据。派生数据（fanout 收件箱、Meili 搜索索引、热榜候选、统计计数）
由既有 consumer 链路自动产生，无需额外补齐。

## 链路

```
wallhaven /api/v1/search + /api/v1/w/{id}   （45 req/min 限速，图片/详情磁盘缓存）
   │  下载图片（默认 large 缩略图）
   ▼
BFF 全链路（模拟真实客户端）：
   Register/Login → UploadMedia → 预签名 URL 直传 MinIO → 轮询资产 ACTIVE
   → CreateDraft（挂资产，首张封面）→ PublishDraft → 轮询帖子 PUBLISHED
   → 互动：Follow / LikePost / FavouritePost / AddComment / ReplyComment
```

## 用法

```bash
# 全量：50 用户 × 500 帖 + 互动（测试服务器）
go run ./scripts/mockgen -bff http://192.0.2.10:8084 -users 50 -posts 500

# 只抓素材看看（不写任何状态、不注入）
go run ./scripts/mockgen -posts 20 -dry-run

# 指定搜索条件
go run ./scripts/mockgen -q "landscape" -categories 100 -atleast 2560x1440 -posts 100
```

## 参数

| 参数 | 默认 | 说明 |
|---|---|---|
| `-bff` | `http://localhost:8084` | BFF 服务地址 |
| `-users` | 50 | mock 用户数（不足自动补注册，超出不建） |
| `-posts` | 500 | 帖子总目标（每帖 1 图；断点续跑精确补足到该数） |
| `-q` | 空 | wallhaven 搜索关键词 |
| `-categories` | `111` | 分类开关 general/anime/people（1 开 0 关） |
| `-purity` | `100` | 纯度开关 sfw/sketchy/nsfw（如 `110`；nsfw 需 API key） |
| `-atleast` | `1920x1080` | 最小分辨率 |
| `-ratios` | 空 | 宽高比过滤，如 `16x9,16x10` |
| `-sorting` | `random` | wallhaven 排序 |
| `-seed` | 自动生成 | 本地文案/作者分配的可复现种子 |
| `-source` | `large` | 图片来源：`full` 原图（数 MB）/ `large` 大缩略图 / `small` 小缩略图 |
| `-tags` | `true` | 抓详情接口补 tags（每帖 1 次 API 调用；关掉提速约 12 分钟/500 帖） |
| `-apikey` | 环境变量 `WALLHAVEN_API_KEY` | wallhaven API key（解锁用户默认过滤器与 nsfw） |
| `-apikey-file` | `./tmp/mockgen/wallhaven.key` | API key 文件（gitignored；优先级：flag > 环境变量 > 文件） |
| `-cache` | `./tmp/mockgen/cache` | 图片 + 详情 JSON 磁盘缓存 |
| `-state` | `./tmp/mockgen/state.json` | 注入进度（断点续跑） |
| `-concurrency` | 6 | 上传/发布并发 worker 数 |
| `-interactions` | `true` | 是否注入互动数据 |
| `-follows/-likes/-favourites/-comments/-replies` | 4/6/2/3/1 | 互动密度 |
| `-dry-run` | `false` | 只抓素材不注入 |

## 生成的文本信息

- **用户**：中文昵称（形容词+名词池 60×60）、简介池 30 条（确定性按邮箱哈希选池，重跑不抖动）。
- **帖子**：标题 28 套模板、正文 18 套多段落模板、尾句 30 变体、话题 `#hashtag`（标签+
  分类+氛围/主题，≤6 个）。
- **metadata（posts.meta_data JSONB）**：wallhaven 原始字段（tags/colors/resolution/
  ratio/category）+ 可选生成文本 `topic`（主题 32）/ `mood`（氛围 32）/ `scene`（场景 12）。
- **评论**：一级评论 60 条 + 楼中楼回复 28 条池，含 emoji/提问/催更风格变体。
- **新词库**：`scenePool`（适用场景）、`seriesPool`（合集名），模板占位符 `{scene}` `{series}`。
- 模板用命名占位符（`{tag}`/`{cat}`/`{res}`/`{mood}`/`{topic}`/`{hashtags}`...）渲染，
  增删模板不受参数个数约束；词库集中在 `pools.go`，渲染逻辑在 `fixture.go`。

## 断点续跑与幂等

- `state.json` 记录用户与每帖进度（wallhaven id → asset/post id），每步原子落盘；
  中断/失败后**重跑同一命令**自动补齐（已发布跳过、失败重试、互动不重复灌评论）。
- **素材清单定档**：wallhaven `sorting=random` 的 `seed` 仅保证同查询页间不重复、
  跨查询不可复现，因此首次抓取后清单存入 `state.json`，续跑回放清单、只补缺口。
- 图片与详情均有磁盘缓存，重跑不再消耗 wallhaven API 配额。

## 注意事项

- wallhaven API 全局限速 **45 req/min**（429 自动指数退避）；500 帖含 tags 详情约需
  12 分钟抓取，属一次性成本。互动阶段不受此限速。
- wallhaven **缩略图恒为 JPEG**（即使原图是 PNG）；工具以实际字节为准生成
  MIME/格式/宽高元数据（consumer `ValidateImage` 会校验一致性）。`-source full` 才与
  `file_type` 一致。
- 互动/发帖全部走幂等面客接口；重复运行关注/点赞不重复计数，评论按 `interacted`
  标记去重。
- **凭据安全**：API key 不入库不进代码，默认从 gitignored 的 `tmp/mockgen/wallhaven.key`
  读取（当前已写入），也可用 `-apikey` / 环境变量 `WALLHAVEN_API_KEY` 注入。
- 生成的账号为 `mockgen-<n>-<seed>@mock.local` / 统一密码 `MockPassw0rd!2026`，
  仅供测试环境使用。
