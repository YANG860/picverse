// Command mockgen — mock 数据生成器：从 wallhaven.cc API 抓取真实壁纸，
// 走 BFF 面客全链路（注册/登录 → 上传 → 草稿 → 发布 → 互动）灌入 mock 用户与帖子。
//
// 派生数据（fanout 收件箱 / Meili 搜索索引 / 热榜候选 / 统计）由既有 consumer 链路
// 自动产生，无需额外补齐；断点续跑幂等（state.json 记录进度，图片本地缓存）。
//
// 用法：
//
//	go run ./scripts/mockgen -users 50 -posts 500
//	go run ./scripts/mockgen -q "landscape" -posts 100 -dry-run
//	go run ./scripts/mockgen -bff http://106.53.172.186:8084 -users 50 -posts 500
//
// wallhaven API 限速 45 req/min，500 帖全量（含 tags 详情）约需 12 分钟抓取；
// 图片与详情均有本地缓存，重跑跳过。详见 README.md。
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	mrand "math/rand"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"rednote/gen/go/bff/v1/bffv1connect"
)

// postPlan 单帖灌注计划（文案与作者在执行前确定，保证固定 seed 可复现）
type postPlan struct {
	wp     whWallpaper
	tags   []string
	title  string
	text   string
	meta   string // posts.meta_data JSONB payload
	author int    // 用户下标
}

type options struct {
	bff         string
	users       int
	posts       int
	q           string
	categories  string
	purity      string
	atleast     string
	ratios      string
	sorting     string
	seed        string
	source      string // 图片来源：full/large/small
	apiKey      string
	apiKeyFile  string // API key 文件（gitignored；flag > 环境变量 > 文件）
	cacheDir    string
	statePath   string
	concurrency int
	tags        bool // 抓取详情接口补 tags（每帖 1 次 API 调用）
	dryRun      bool
	interact    bool
	interactCfg interactConfig
}

func parseOptions() options {
	var o options
	flag.StringVar(&o.bff, "bff", "http://localhost:8084", "BFF 服务地址")
	flag.IntVar(&o.users, "users", 50, "mock 用户数")
	flag.IntVar(&o.posts, "posts", 500, "mock 帖子总数（每帖 1 图，断点续跑时补足到该数）")
	flag.StringVar(&o.q, "q", "", "wallhaven 搜索关键词（留空走全站随机）")
	flag.StringVar(&o.categories, "categories", "111", "wallhaven 分类开关（general/anime/people，1 开 0 关）")
	flag.StringVar(&o.purity, "purity", "100", "wallhaven 纯度开关（sfw/sketchy/nsfw，如 110；nsfw 需 API key）")
	flag.StringVar(&o.atleast, "atleast", "1920x1080", "wallhaven 最小分辨率过滤")
	flag.StringVar(&o.ratios, "ratios", "", "wallhaven 宽高比过滤，如 16x9,16x10")
	flag.StringVar(&o.sorting, "sorting", "random", "wallhaven 排序：date_added/relevance/random/views/favorites/toplist")
	flag.StringVar(&o.seed, "seed", "", "随机种子（wallhaven random + 本地文案生成共用；留空自动生成）")
	flag.StringVar(&o.source, "source", "large", "图片来源：full=原图 / large=大缩略图 / small=小缩略图")
	flag.StringVar(&o.apiKey, "apikey", "", "wallhaven API key（默认读环境变量 WALLHAVEN_API_KEY，再读 -apikey-file）")
	flag.StringVar(&o.apiKeyFile, "apikey-file", "./tmp/mockgen/wallhaven.key", "wallhaven API key 文件（gitignored，避免凭据入库）")
	flag.StringVar(&o.cacheDir, "cache", "./tmp/mockgen/cache", "抓取缓存目录（图片 + 详情 JSON）")
	flag.StringVar(&o.statePath, "state", "./tmp/mockgen/state.json", "注入进度状态文件（断点续跑）")
	flag.IntVar(&o.concurrency, "concurrency", 6, "并发 worker 数（上传+发布流水线）")
	flag.BoolVar(&o.tags, "tags", true, "抓详情接口补 tags（关掉提速约 12 分钟，metadata 无 tags）")
	flag.BoolVar(&o.dryRun, "dry-run", false, "只抓取 wallhaven 素材，不注入 BFF")
	flag.BoolVar(&o.interact, "interactions", true, "注入互动数据（关注/点赞/收藏/评论）")
	flag.IntVar(&o.interactCfg.Follows, "follows", 4, "每用户随机关注数")
	flag.IntVar(&o.interactCfg.Likes, "likes", 6, "每帖随机点赞用户数")
	flag.IntVar(&o.interactCfg.Favourites, "favourites", 2, "每帖随机收藏用户数")
	flag.IntVar(&o.interactCfg.Comments, "comments", 3, "每帖一级评论数")
	flag.IntVar(&o.interactCfg.Replies, "replies", 1, "每帖楼中楼回复数")
	flag.Parse()

	if o.apiKey == "" {
		o.apiKey = os.Getenv("WALLHAVEN_API_KEY")
	}
	if o.apiKey == "" && o.apiKeyFile != "" {
		// 密钥文件兜底（gitignored）：flag > 环境变量 > 文件
		if data, err := os.ReadFile(o.apiKeyFile); err == nil {
			o.apiKey = strings.TrimSpace(string(data))
		}
	}
	return o
}

// validate 校验参数边界，非法直接退出（fail-fast，避免半程失败）
func (o options) validate() error {
	if o.users < 1 {
		return fmt.Errorf("-users 必须 ≥ 1，当前 %d", o.users)
	}
	if o.posts < 1 {
		return fmt.Errorf("-posts 必须 ≥ 1，当前 %d", o.posts)
	}
	if o.concurrency < 1 || o.concurrency > 32 {
		return fmt.Errorf("-concurrency 取值 1~32，当前 %d", o.concurrency)
	}
	if o.seed != "" {
		if len(o.seed) != 6 {
			return fmt.Errorf("-seed 须为 [a-zA-Z0-9]{6}（wallhaven 契约），当前 %q", o.seed)
		}
		for _, c := range o.seed {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
				return fmt.Errorf("-seed 须为 [a-zA-Z0-9]{6}（wallhaven 契约），当前 %q", o.seed)
			}
		}
	}
	if o.purity != "" {
		if len(o.purity) != 3 {
			return fmt.Errorf("-purity 须为 3 位 0/1 开关（sfw/sketchy/nsfw），当前 %q", o.purity)
		}
		for _, c := range o.purity {
			if c != '0' && c != '1' {
				return fmt.Errorf("-purity 须为 3 位 0/1 开关（sfw/sketchy/nsfw），当前 %q", o.purity)
			}
		}
	}
	switch o.source {
	case "full", "large", "small":
	default:
		return fmt.Errorf("-source 取值 full/large/small，当前 %q", o.source)
	}
	return nil
}

// genSeed 生成 wallhaven random 排序种子（[a-zA-Z0-9]{6}）
func genSeed() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 6)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			// crypto/rand 极少失败；退化到时间戳派生，保证流程可继续
			return fmt.Sprintf("%06x", time.Now().UnixNano()%0xffffff)
		}
		b[i] = alphabet[n.Int64()]
	}
	return string(b)
}

// collectWallpapers 收集候选壁纸：搜索翻页 + 可选详情补 tags（详情走磁盘缓存）。
// skip 为已入清单的 wallhaven id（random 排序跨查询可能撞车重复，需去重）。
// 注意：wallhaven 的 seed 仅保证同一次查询的页间不重复，跨查询不可复现，
// 因此续跑必须由调用方回放 state 清单而非重新搜索。
func collectWallpapers(ctx context.Context, wh *whClient, o options, need int, skip map[string]bool) ([]whWallpaper, error) {
	var out []whWallpaper
	seen := map[string]bool{}
	for id := range skip {
		seen[id] = true
	}
	for page := 1; len(out) < need; page++ {
		items, meta, err := wh.Search(ctx, whSearchParams{
			Q:          o.q,
			Categories: o.categories,
			Purity:     o.purity,
			Sorting:    o.sorting,
			Atleast:    o.atleast,
			Ratios:     o.ratios,
			Seed:       o.seed,
			Page:       page,
		})
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			fmt.Printf("wallhaven 第 %d 页无结果，停止翻页（新收集 %d 条）\n", page, len(out))
			break
		}
		for _, it := range items {
			if seen[it.ID] {
				continue
			}
			seen[it.ID] = true
			out = append(out, it)
			if len(out) >= need {
				break
			}
		}
		if meta.LastPage > 0 && page >= int(meta.LastPage) {
			fmt.Printf("wallhaven 已到最后一页 %d（%s），新收集 %d 条\n", meta.LastPage, meta.Query, len(out))
			break
		}
	}

	if !o.tags {
		return out, nil
	}
	// 详情补 tags（列表接口不返回）；逐条走磁盘缓存 + 全局限速，断点续跑秒过
	for i := range out {
		d, err := loadCachedDetail(ctx, wh, o.cacheDir, out[i].ID)
		if err != nil {
			// 详情失败不致命：降级为无 tags 条目，保留列表字段
			fmt.Printf("警告: wallhaven %s 详情获取失败（降级无 tags）: %v\n", out[i].ID, err)
			continue
		}
		out[i].Tags = d.Tags
	}
	return out, nil
}

// buildPlans 生成灌注计划：文案合成 + 作者分配（固定 seed 可复现）
func buildPlans(rng *mrand.Rand, wallpapers []whWallpaper, users int) ([]*postPlan, error) {
	plans := make([]*postPlan, 0, len(wallpapers))
	for i, wp := range wallpapers {
		tags := whTagsNames(wp)
		meta := map[string]any{
			"source":        "wallhaven",
			"wallhaven_id":  wp.ID,
			"wallhaven_url": wp.URL,
			"category":      wp.Category,
			"purity":        wp.Purity,
			"resolution":    wp.Resolution,
			"ratio":         wp.Ratio,
			"colors":        wp.Colors,
			"tags":          tags,
			// 可选生成文本：主题/氛围，丰富 metadata 检索与展示维度
			"topic": randTopic(rng),
			"mood":  randMood(rng),
		}
		metaJSON, err := json.Marshal(meta)
		if err != nil {
			return nil, fmt.Errorf("序列化帖子元数据（wallhaven %s）: %w", wp.ID, err)
		}
		plans = append(plans, &postPlan{
			wp:     wp,
			tags:   tags,
			title:  randTitle(rng, wp, tags),
			text:   randText(rng, wp, tags),
			meta:   string(metaJSON),
			author: i % users,
		})
	}
	return plans, nil
}

// ensureUsers 补足用户数并建立会话（已注册直接登录，断点续跑幂等）
func ensureUsers(ctx context.Context, client bffv1connect.BFFServiceClient, st *mockState, o options, rng *mrand.Rand) ([]*bffSession, error) {
	for i := len(st.Users); i < o.users; i++ {
		st.Users = append(st.Users, &userState{
			Email: fmt.Sprintf("mockgen-%d-%s@mock.local", i, o.seed),
			Name:  randName(rng, i),
		})
	}
	sessions := make([]*bffSession, 0, len(st.Users))
	for _, us := range st.Users {
		s, err := ensureSession(ctx, client, us)
		if err != nil {
			return nil, err
		}
		// 写入确定性简介（丰富 profile 文案；失败不阻断，下轮重跑补齐）
		if err := s.updateBio(ctx, bioFor(us.Email)); err != nil {
			fmt.Printf("警告: 用户 %s 简介写入失败: %v\n", us.Email, err)
		}
		sessions = append(sessions, s)
	}
	if err := st.save(); err != nil {
		return nil, err
	}
	return sessions, nil
}

// injectPosts 并发灌注帖子：上传图片 → 建草稿 → 发布 → 等 PUBLISHED，逐帖落盘进度
func injectPosts(ctx context.Context, o options, st *mockState, sessions []*bffSession, plans []*postPlan, images map[string]*cachedImage) ([]string, int) {
	type job struct {
		plan *postPlan
	}
	jobs := make(chan job)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var published []string
	failed := 0

	worker := func() {
		defer wg.Done()
		for j := range jobs {
			plan := j.plan
			author := sessions[plan.author]
			img := images[plan.wp.ID]
			postID, err := author.injectPost(ctx, plan, img, st)
			mu.Lock()
			if err != nil {
				failed++
				fmt.Printf("错误: 帖子注入失败（wallhaven %s, 作者 %s）: %v\n", plan.wp.ID, author.email, err)
			} else {
				published = append(published, postID)
			}
			mu.Unlock()
		}
	}
	wg.Add(o.concurrency)
	for i := 0; i < o.concurrency; i++ {
		go worker()
	}
	for _, p := range plans {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return published, failed
		case jobs <- job{plan: p}:
		}
	}
	close(jobs)
	wg.Wait()
	return published, failed
}

func main() {
	o := parseOptions()
	if err := o.validate(); err != nil {
		fmt.Fprintln(os.Stderr, "参数错误:", err)
		os.Exit(1)
	}
	if o.seed == "" {
		o.seed = genSeed()
		fmt.Println("生成随机种子:", o.seed, "（-seed 控制本地文案/作者分配的可复现性）")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rng := mrand.New(mrand.NewSource(int64(fnv32(o.seed))))
	wh := newWHClient(o.apiKey)

	// ---- 1. 素材清单：dry-run 不写状态；正式运行以 state 清单回放为准 ----
	//（wallhaven random 排序跨查询不可复现，清单定档后缺口补抓，精确补足到 -posts）
	if o.dryRun {
		fmt.Printf("== dry-run：抓取 wallhaven 素材 %d 条（q=%q, sorting=%s）==\n", o.posts, o.q, o.sorting)
		wallpapers, err := collectWallpapers(ctx, wh, o, o.posts, nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, "抓取 wallhaven 失败:", err)
			os.Exit(1)
		}
		images, _ := downloadAll(ctx, wh, o, wallpapers)
		fmt.Printf("图片就绪 %d / %d\n", len(images), len(wallpapers))
		fmt.Println("dry-run：跳过 BFF 注入。素材样例（含生成文案）：")
		for i, wp := range wallpapers {
			if i >= 3 {
				break
			}
			tags := whTagsNames(wp)
			fmt.Printf("  %s %s %s tags=%v\n", wp.ID, wp.Resolution, wp.Category, tags)
			fmt.Printf("    标题: %s\n", randTitle(rng, wp, tags))
			fmt.Printf("    正文: %s\n", strings.ReplaceAll(randText(rng, wp, tags), "\n", " / "))
		}
		return
	}

	st, err := loadState(o.statePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "加载状态失败:", err)
		os.Exit(1)
	}
	if need := o.posts - len(st.Candidates); need > 0 {
		fmt.Printf("== 抓取 wallhaven 素材：新增 %d 条（清单已有 %d）==\n", need, len(st.Candidates))
		skip := map[string]bool{}
		for _, c := range st.Candidates {
			skip[c.ID] = true
		}
		fresh, err := collectWallpapers(ctx, wh, o, need, skip)
		if err != nil {
			fmt.Fprintln(os.Stderr, "抓取 wallhaven 失败:", err)
			os.Exit(1)
		}
		if err := st.appendCandidates(fresh); err != nil {
			fmt.Fprintln(os.Stderr, "素材清单落盘失败:", err)
			os.Exit(1)
		}
	}
	if len(st.Candidates) == 0 {
		fmt.Fprintln(os.Stderr, "wallhaven 无可用素材，退出")
		os.Exit(1)
	}
	wallpapers := st.Candidates
	fmt.Printf("素材清单 %d 条，下载图片（source=%s）\n", len(wallpapers), o.source)
	images, dlFailed := downloadAll(ctx, wh, o, wallpapers)
	fmt.Printf("图片就绪 %d / %d（失败 %d）\n", len(images), len(wallpapers), dlFailed)

	// ---- 2. 建立用户会话（注册/登录）----
	client := bffv1connect.NewBFFServiceClient(http.DefaultClient, o.bff)
	fmt.Printf("== 建立 %d 个 mock 用户会话（bff=%s）==\n", o.users, o.bff)
	sessions, err := ensureUsers(ctx, client, st, o, rng)
	if err != nil {
		fmt.Fprintln(os.Stderr, "建立用户会话失败:", err)
		os.Exit(1)
	}

	// ---- 3. 生成计划并灌注帖子（预算 = 目标数 - 已发布数，未发布项自动重试）----
	plans, err := buildPlans(rng, wallpapers, o.users)
	if err != nil {
		fmt.Fprintln(os.Stderr, "生成灌注计划失败:", err)
		os.Exit(1)
	}
	budget := o.posts - len(st.publishedPostIDs())
	todo := plans[:0:0]
	for _, p := range plans {
		if budget <= 0 {
			break
		}
		if images[p.wp.ID] == nil {
			continue // 图片下载失败的条目跳过（步骤 1 已告警）
		}
		if ps, ok := st.peekPost(p.wp.ID); ok && ps.Published {
			continue // 断点续跑：已发布跳过
		}
		todo = append(todo, p)
		budget--
	}
	fmt.Printf("== 灌注帖子：本次 %d / 总目标 %d（已发布跳过）==\n", len(todo), o.posts)
	newPosts, failed := injectPosts(ctx, o, st, sessions, todo, images)
	fmt.Printf("本次发布成功 %d，失败 %d\n", len(newPosts), failed)

	// ---- 4. 互动数据（关注/点赞/收藏/评论）----
	if o.interact {
		postIDs, whIDs := st.pendingInteractPostIDs()
		fmt.Printf("== 注入互动数据：帖子 %d 个，关注 %d/用户 ==\n", len(postIDs), o.interactCfg.Follows)
		stats := runInteractions(ctx, rng, o.interactCfg, sessions, postIDs)
		fmt.Printf("互动完成：关注 %d 点赞 %d 收藏 %d 评论 %d 回复 %d（失败 %d）\n",
			stats.Follows, stats.Likes, stats.Favourites, stats.Comments, stats.Replies, stats.Errors)
		if err := st.markInteracted(whIDs); err != nil {
			fmt.Println("警告: 互动进度落盘失败（重跑会重复灌评论）:", err)
		}
	}

	// ---- 5. 汇总 ----
	fmt.Println("== 完成 ==")
	fmt.Printf("用户 %d，帖子 %d/%d，状态文件 %s\n", len(st.Users), len(st.publishedPostIDs()), o.posts, o.statePath)
	if failed > 0 {
		fmt.Println("注意：存在失败项，重跑同一命令可自动补齐（幂等）")
	}
}

// downloadAll 并发下载 wallhaven 图片（API 之外的 CDN 流量），返回就绪图片与失败数
func downloadAll(ctx context.Context, wh *whClient, o options, wallpapers []whWallpaper) (map[string]*cachedImage, int) {
	var mu sync.Mutex
	images := map[string]*cachedImage{}
	failed := 0
	sem := make(chan struct{}, o.concurrency)
	var wg sync.WaitGroup
	for _, wp := range wallpapers {
		wg.Add(1)
		sem <- struct{}{}
		go func(wp whWallpaper) {
			defer wg.Done()
			defer func() { <-sem }()
			img, err := ensureImage(ctx, wh, o.cacheDir, wp, o.source)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed++
				fmt.Printf("警告: wallhaven %s 图片就绪失败: %v\n", wp.ID, err)
				return
			}
			images[wp.ID] = img
		}(wp)
	}
	wg.Wait()
	return images, failed
}

// fnv32 字符串种子 → int64（本地 RNG 可复现）
func fnv32(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}
