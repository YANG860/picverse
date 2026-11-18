// wallhaven.cc API v1 客户端（契约见 https://wallhaven.cc/help/api）：
// 搜索列表 / 单图详情 / 图片下载。
// API 全局限速 45 req/min（超出返回 429），客户端用串行令牌桶自限速；
// 图片下载走 CDN（w/th.wallhaven.cc），不占 API 配额，仅限并发。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	whAPIBase = "https://wallhaven.cc/api/v1"
	// whMinInterval API 最小请求间隔：60s / 45 ≈ 1.34s（留 5% 余量）
	whMinInterval = time.Second*60/45 + 80*time.Millisecond
)

// whTag wallhaven 标签（仅详情接口返回）
type whTag struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Alias    string `json:"alias"`
	Category string `json:"category"`
	Purity   string `json:"purity"`
}

// whWallpaper wallhaven 壁纸条目（列表接口字段全集；Tags 仅详情接口填充）
type whWallpaper struct {
	ID         string   `json:"id"`
	URL        string   `json:"url"`
	ShortURL   string   `json:"short_url"`
	Views      int64    `json:"views"`
	Favorites  int64    `json:"favorites"`
	Purity     string   `json:"purity"`
	Category   string   `json:"category"`
	DimensionX int64    `json:"dimension_x"`
	DimensionY int64    `json:"dimension_y"`
	Resolution string   `json:"resolution"`
	Ratio      string   `json:"ratio"`
	FileSize   int64    `json:"file_size"`
	FileType   string   `json:"file_type"`
	CreatedAt  string   `json:"created_at"`
	Colors     []string `json:"colors"`
	Path       string   `json:"path"`
	Thumbs     struct {
		Large    string `json:"large"`
		Original string `json:"original"`
		Small    string `json:"small"`
	} `json:"thumbs"`
	Tags []whTag `json:"tags"`
}

// flexInt wallhaven meta 数字字段容错解析：部分响应（如带 apikey）把数字
// 序列化为字符串，数字/字符串两种形态都要兼容
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	var n int64
	if err := json.Unmarshal(b, &n); err == nil {
		*f = flexInt(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("meta 数字字段既非数字也非字符串: %s", b)
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("解析 meta 数字字符串 %q: %w", s, err)
	}
	*f = flexInt(v)
	return nil
}

type whSearchMeta struct {
	CurrentPage flexInt `json:"current_page"`
	LastPage    flexInt `json:"last_page"`
	PerPage     flexInt `json:"per_page"`
	Total       flexInt `json:"total"`
	Query       string  `json:"query"`
	Seed        string  `json:"seed"`
}

// whSearchParams 搜索参数（purity 默认 SFW；sketchy/nsfw 需带 API key）
type whSearchParams struct {
	Q          string
	Categories string // 三位开关，如 "111"（general/anime/people）
	Purity     string // 三位开关，如 "100"（sfw/sketchy/nsfw），留空默认 100
	Sorting    string // date_added/relevance/random/views/favorites/toplist
	Order      string
	Atleast    string // 最小分辨率，如 "1920x1080"
	Ratios     string // 如 "16x9,16x10"
	Seed       string // random 排序的可复现种子，[a-zA-Z0-9]{6}
	Page       int
}

type whClient struct {
	apiKey string
	api    *http.Client
	dl     *http.Client

	mu   sync.Mutex
	next time.Time // 下次允许 API 请求的时间（串行令牌桶）
}

func newWHClient(apiKey string) *whClient {
	return &whClient{
		apiKey: apiKey,
		api:    &http.Client{Timeout: 30 * time.Second},
		dl:     &http.Client{Timeout: 120 * time.Second},
	}
}

// throttle API 串行限速（45 req/min），带 ±10% 抖动防同步踩点
func (c *whClient) throttle() {
	c.mu.Lock()
	now := time.Now()
	wait := c.next.Sub(now)
	interval := time.Duration(float64(whMinInterval) * (0.9 + 0.2*rand.Float64()))
	if wait < 0 {
		wait = 0
	}
	c.next = now.Add(wait + interval)
	c.mu.Unlock()
	if wait > 0 {
		time.Sleep(wait)
	}
}

// getJSON 限速 GET + 429/5xx 指数退避重试（最多 3 次）
func (c *whClient) getJSON(ctx context.Context, endpoint string, params url.Values, out any) error {
	u := whAPIBase + endpoint
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		c.throttle()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return fmt.Errorf("构造 wallhaven 请求 %s: %w", u, err)
		}
		req.Header.Set("User-Agent", "rednote-mockgen/1.0")
		resp, err := c.api.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("请求 wallhaven %s: %w", u, err)
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("读取 wallhaven 响应 %s: %w", u, err)
			continue
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			if err := json.Unmarshal(body, out); err != nil {
				return fmt.Errorf("解析 wallhaven 响应 %s: %w（body 前 200 字节: %.200s）", u, err, body)
			}
			return nil
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("wallhaven %s 返回 %d（body 前 200 字节: %.200s）", u, resp.StatusCode, body)
			time.Sleep(time.Duration(attempt+1) * 20 * time.Second) // 退避：20s / 40s
		default:
			return fmt.Errorf("wallhaven %s 返回 %d（body 前 200 字节: %.200s）", u, resp.StatusCode, body)
		}
	}
	return fmt.Errorf("wallhaven 重试 3 次仍失败: %w", lastErr)
}

// Search 分页搜索（每页最多 24 条），返回条目与分页元信息
func (c *whClient) Search(ctx context.Context, p whSearchParams) ([]whWallpaper, whSearchMeta, error) {
	params := url.Values{}
	set := func(k, v string) {
		if v != "" {
			params.Set(k, v)
		}
	}
	set("q", p.Q)
	set("categories", p.Categories)
	set("purity", p.Purity)
	set("sorting", p.Sorting)
	set("order", p.Order)
	set("atleast", p.Atleast)
	set("ratios", p.Ratios)
	set("seed", p.Seed)
	set("apikey", c.apiKey) // 携带 API key 解锁用户默认过滤器与 sketchy/nsfw
	params.Set("page", strconv.Itoa(p.Page))

	var raw struct {
		Data []whWallpaper `json:"data"`
		Meta whSearchMeta  `json:"meta"`
	}
	if err := c.getJSON(ctx, "/search", params, &raw); err != nil {
		return nil, whSearchMeta{}, fmt.Errorf("搜索 wallhaven: %w", err)
	}
	return raw.Data, raw.Meta, nil
}

// Detail 单图详情（含 tags，列表接口不返回），用于生成更真实的标题/正文/元数据
func (c *whClient) Detail(ctx context.Context, id string) (whWallpaper, error) {
	params := url.Values{}
	if c.apiKey != "" {
		params.Set("apikey", c.apiKey)
	}
	var raw struct {
		Data whWallpaper `json:"data"`
	}
	if err := c.getJSON(ctx, "/w/"+url.PathEscape(id), params, &raw); err != nil {
		return whWallpaper{}, fmt.Errorf("获取 wallhaven 详情 %s: %w", id, err)
	}
	return raw.Data, nil
}

// Download 下载图片到 dest（CDN 直链，不占 API 配额）
func (c *whClient) Download(ctx context.Context, src, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return fmt.Errorf("构造下载请求 %s: %w", src, err)
	}
	req.Header.Set("User-Agent", "rednote-mockgen/1.0")
	resp, err := c.dl.Do(req)
	if err != nil {
		return fmt.Errorf("下载图片 %s: %w", src, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载图片 %s 返回 %d", src, resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("创建本地图片文件 %s: %w", dest, err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(dest)
		return fmt.Errorf("写入本地图片文件 %s: %w", dest, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("关闭本地图片文件 %s: %w", dest, err)
	}
	return nil
}

// whTagsNames 提取标签名列表（去重保序）
func whTagsNames(w whWallpaper) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(w.Tags))
	for _, t := range w.Tags {
		name := strings.TrimSpace(t.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}
