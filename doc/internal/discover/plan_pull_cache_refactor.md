# 临时计划：pull 判据重构为「缓存存在性」（已落地 2025-XX，可删除）

> 状态：**已落地**。首帖 race 不处理（稍等刷新即可，可接受）；表直接删除（无生产库，dbinit 重建测试模板即可）。
> 零漏前提：`cache.timeline_ttl` ≥ `fanout.retention`（均已 30d）。

## 定稿语义（替换现有名单表/宽限期方案）

| 环节 | 判据 | 动作 |
|---|---|---|
| 写侧（发帖） | `followers >= bigv.promote_followers`（5000，单阈值、无滞回） | 是 → `AuthorTimelineCache.Append`；否 → 拉粉丝 `FanoutRepo.WriteInbox` |
| 读侧（pull 归并） | **缓存 key 存在**（`Window` miss 分流：未缓存 → 收件箱路；见底/短取 → 回源 DB + `Rebuild`） | 有缓存的作者（现任大V + 刚降级缓存未清）自动被 pull 覆盖 |
| 降级（跌破线） | 无动作 | 缓存随 TTL（30d）自然消亡，自动回归纯收件箱 |
| 晋升首帖 race | **不处理**（用户拍板） | key 建立前若粉丝读到，稍等刷新即可 |

### 零漏论证（状态转移，缓存 key 存活 ≥ 30d 时）

| 状态 | 帖子去向 | 供给线 | 漏吗 |
|---|---|---|---|
| 普通作者 / 晋升前 | fanout | 收件箱（30d） | 不漏（寿命语义一致） |
| 大V期 | 只写缓存 | key 在 → pull（miss 回源 DB） | 不漏 |
| 刚降级（key 未清） | 新帖 fanout、旧帖在缓存 | 双路（pull 回源拉到新帖，归并去重兜重） | 不漏 |
| 降级 key 过期后 | 新帖 fanout | 收件箱 | 不漏（旧帖已过寿命） |
| 粉丝数抖动（无滞回） | 随机走一路 | 各有一条供给线 | 不漏（仅双写重，去重兜底） |

漏帖的唯一残余风险 = **Redis 数据丢失**（故障/淘汰）导致 key 提前消亡——基础设施风险，接受。

## 变更清单（已全部落地）

1. **TTL 对齐**：`CacheTimelineTTL` 7d → 30d（= `fanout.retention`）；`config.yaml`/`config.md` 同步 ✅
2. **写侧简化**：`dispatchPublishFanout` 塌缩为阈值比较，删 `BigVJudge` 接口 ✅
   - 落地时追加修正：**Append 失败 fallback 全量扇出**（新口径 miss=收件箱路，帖子必须有收件箱供给线，
     不允许因缓存故障丢供给；旧 fail-open 语义已废弃）。
3. **大V状态整体退役**：`BigVRepo`/`JudgePublish`/`BigVAuthor` 模型删除；`sql/008` 文件整个删除
  （无生产库，`go run ./scripts/dbinit` 重建测试模板即清理，无 010 migration）✅
4. **读侧分流契约**：`Window` miss 分流（miss=true → 收件箱路不回源；miss=false 短取 → pull 路回源+Rebuild），
   注释与 `TestAuthorTimelineWindowMiss` 锁定 ✅
5. **测试调整**：删宽限期用例（bigv_repo_test.go 随 BigVRepo 删除），判定改为阈值分流断言，
   新增 `TestHandlePublishFanout_AppendFallback` ✅
6. **文档**：`discover.md` 重写（零漏论证表/分流契约）、`config.md`/`sql/index.md`（11 表 15 索引）✅

## 明确不做

- 晋升首帖 race 防护（用户拍板接受）；
- 首帖主动 Rebuild 预热（读侧 miss 自愈已够）；
- 滞回双阈值（抖动双写由归并去重兜底，已论证不漏）；
- `GetFollowingFeed` 归并接线（另行 P1，用本计划的 miss 分流契约）。
