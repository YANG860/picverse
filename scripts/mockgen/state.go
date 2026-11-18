// mockgen 状态持久化：记录已注册用户与已注入帖子（wallhaven id → asset/post id），
// 支持断点续跑与幂等——重跑只补缺失项，不重复注册/发帖。
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// userState 已注册 mock 用户（令牌不落盘，重跑时重新登录换发）
type userState struct {
	Email string `json:"email"`
	Name  string `json:"name"`
	UID   string `json:"uid"`
}

// postState 单帖注入进度（key = wallhaven id，便于溯源）
type postState struct {
	AssetID    string `json:"asset_id,omitempty"`
	PostID     string `json:"post_id,omitempty"`
	Published  bool   `json:"published"`
	Interacted bool   `json:"interacted,omitempty"` // 互动已注入（重跑不重复灌评论）
	Err        string `json:"err,omitempty"`
}

type mockState struct {
	path  string
	mu    sync.Mutex
	Users []*userState          `json:"users"`
	Posts map[string]*postState `json:"posts"`
	// Candidates 素材清单（首次抓取后定档）：wallhaven random 排序的 seed 仅保证
	// 页间不重复、跨查询不可复现，续跑必须回放同一清单才能精确补足到目标数
	Candidates []whWallpaper `json:"candidates,omitempty"`
}

// loadState 加载状态文件；不存在返回空状态（首次运行）
func loadState(path string) (*mockState, error) {
	s := &mockState{path: path, Posts: map[string]*postState{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取状态文件 %s: %w", path, err)
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("解析状态文件 %s（可删除后重跑）: %w", path, err)
	}
	if s.Posts == nil {
		s.Posts = map[string]*postState{}
	}
	s.path = path
	return s, nil
}

// save 原子落盘（temp + rename），每完成一步调用，崩溃后可续跑
func (s *mockState) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化状态: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("创建状态目录 %s: %w", filepath.Dir(s.path), err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入状态临时文件 %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("替换状态文件 %s: %w", s.path, err)
	}
	return nil
}

// peekPost 只读查询帖状态（不创建空条目）
func (s *mockState) peekPost(whID string) (postState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.Posts[whID]
	if !ok {
		return postState{}, false
	}
	return *p, true
}

// setPost 覆写帖状态并立即落盘（每帖完成即持久化，崩溃后可续跑）
func (s *mockState) setPost(whID string, p *postState) error {
	s.mu.Lock()
	s.Posts[whID] = p
	s.mu.Unlock()
	return s.save()
}

// appendCandidates 追加素材清单并落盘（清单定档后不得变更）
func (s *mockState) appendCandidates(items []whWallpaper) error {
	s.mu.Lock()
	s.Candidates = append(s.Candidates, items...)
	s.mu.Unlock()
	return s.save()
}

// publishedPostIDs 已发布帖的 post_id 列表（互动阶段用）
func (s *mockState) publishedPostIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.Posts))
	for _, p := range s.Posts {
		if p.Published && p.PostID != "" {
			out = append(out, p.PostID)
		}
	}
	return out
}

// pendingInteractPostIDs 待注入互动的帖子（已发布且未互动），并行返回 post_id 与 wallhaven id
func (s *mockState) pendingInteractPostIDs() (postIDs, whIDs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for whID, p := range s.Posts {
		if p.Published && !p.Interacted && p.PostID != "" {
			postIDs = append(postIDs, p.PostID)
			whIDs = append(whIDs, whID)
		}
	}
	return postIDs, whIDs
}

// markInteracted 标记帖子互动已注入并落盘（幂等：重跑不重复灌评论）
func (s *mockState) markInteracted(whIDs []string) error {
	s.mu.Lock()
	for _, id := range whIDs {
		if p, ok := s.Posts[id]; ok {
			p.Interacted = true
		}
	}
	s.mu.Unlock()
	return s.save()
}
