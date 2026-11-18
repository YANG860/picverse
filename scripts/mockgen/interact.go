// 互动数据注入：用 mock 用户互相 Follow，并对已发布帖子点赞/收藏/评论/楼中楼，
// 为热榜（热度分）、推荐流（打散/去重）、搜索（Meili）制造有分布的真实样本。
// 互动均为幂等接口，失败记录上下文并继续，不中断整体灌注。
package main

import (
	"context"
	"fmt"
	"math/rand"

	"connectrpc.com/connect"

	bffv1 "rednote/gen/go/bff/v1"
)

// interactConfig 互动规模参数
type interactConfig struct {
	Follows    int // 每用户随机关注数
	Likes      int // 每帖随机点赞用户数
	Favourites int // 每帖随机收藏用户数
	Comments   int // 每帖一级评论数
	Replies    int // 每帖楼中楼回复数
}

type interactStats struct {
	Follows    int
	Likes      int
	Favourites int
	Comments   int
	Replies    int
	Errors     int
}

// pickUsers 从会话池随机抽 n 个（可重复；互动动作对重复用户幂等，不影响计数正确性）
func pickUsers(rng *rand.Rand, sessions []*bffSession, n int) []*bffSession {
	out := make([]*bffSession, 0, n)
	for i := 0; i < n && len(sessions) > 0; i++ {
		out = append(out, sessions[rng.Intn(len(sessions))])
	}
	return out
}

// runInteractions 注入关注关系 + 帖子互动；返回成功计数统计
func runInteractions(ctx context.Context, rng *rand.Rand, cfg interactConfig, sessions []*bffSession, postIDs []string) interactStats {
	var st interactStats

	// ---- 1. 关注关系：每用户随机关注 cfg.Follows 个他人（跳过自己）----
	for _, s := range sessions {
		followed := 0
		for attempt := 0; attempt < cfg.Follows*3 && followed < cfg.Follows; attempt++ {
			t := sessions[rng.Intn(len(sessions))]
			if t.uid == s.uid {
				continue
			}
			resp, err := s.client.Follow(ctx, withBearer(connect.NewRequest(
				&bffv1.FollowRequest{TargetUid: t.uid}), s.token))
			if err != nil {
				st.Errors++
				fmt.Printf("警告: %s 关注 %s RPC 失败: %v\n", s.email, t.uid, err)
				continue
			}
			if resp.Msg.GetFail() {
				st.Errors++
				fmt.Printf("警告: %s 关注 %s 业务失败: code=%d msg=%s\n", s.email, t.uid, resp.Msg.GetErr().GetCode(), resp.Msg.GetErr().GetMsg())
				continue
			}
			followed++
			st.Follows++
		}
	}

	// ---- 2. 帖子互动：点赞/收藏/评论/楼中楼 ----
	for _, postID := range postIDs {
		for _, u := range pickUsers(rng, sessions, cfg.Likes) {
			resp, err := u.client.LikePost(ctx, withBearer(connect.NewRequest(
				&bffv1.LikePostRequest{PostId: postID}), u.token))
			if err != nil {
				st.Errors++
				fmt.Printf("警告: %s 点赞帖 %s RPC 失败: %v\n", u.email, postID, err)
				continue
			}
			if resp.Msg.GetFail() {
				st.Errors++
				fmt.Printf("警告: %s 点赞帖 %s 业务失败: code=%d msg=%s\n", u.email, postID, resp.Msg.GetErr().GetCode(), resp.Msg.GetErr().GetMsg())
				continue
			}
			st.Likes++
		}

		for _, u := range pickUsers(rng, sessions, cfg.Favourites) {
			resp, err := u.client.FavouritePost(ctx, withBearer(connect.NewRequest(
				&bffv1.FavouritePostRequest{PostId: postID}), u.token))
			if err != nil {
				st.Errors++
				fmt.Printf("警告: %s 收藏帖 %s RPC 失败: %v\n", u.email, postID, err)
				continue
			}
			if resp.Msg.GetFail() {
				st.Errors++
				fmt.Printf("警告: %s 收藏帖 %s 业务失败: code=%d msg=%s\n", u.email, postID, resp.Msg.GetErr().GetCode(), resp.Msg.GetErr().GetMsg())
				continue
			}
			st.Favourites++
		}

		// 一级评论（记录 comment_id 供楼中楼回复）
		rootIDs := make([]string, 0, cfg.Comments)
		for _, u := range pickUsers(rng, sessions, cfg.Comments) {
			resp, err := u.client.AddComment(ctx, withBearer(connect.NewRequest(
				&bffv1.AddCommentRequest{PostId: postID, Content: randComment(rng)}), u.token))
			if err != nil {
				st.Errors++
				fmt.Printf("警告: %s 评论帖 %s RPC 失败: %v\n", u.email, postID, err)
				continue
			}
			if resp.Msg.GetFail() {
				st.Errors++
				fmt.Printf("警告: %s 评论帖 %s 业务失败: code=%d msg=%s\n", u.email, postID, resp.Msg.GetErr().GetCode(), resp.Msg.GetErr().GetMsg())
				continue
			}
			st.Comments++
			if id := resp.Msg.GetComment().GetCommentId(); id != "" {
				rootIDs = append(rootIDs, id)
			}
		}

		// 楼中楼：挂到随机楼层
		for _, u := range pickUsers(rng, sessions, cfg.Replies) {
			if len(rootIDs) == 0 {
				break
			}
			root := rootIDs[rng.Intn(len(rootIDs))]
			resp, err := u.client.ReplyComment(ctx, withBearer(connect.NewRequest(
				&bffv1.ReplyCommentRequest{PostId: postID, ParentId: root, Content: randReply(rng)}), u.token))
			if err != nil {
				st.Errors++
				fmt.Printf("警告: %s 回复帖 %s 楼 %s RPC 失败: %v\n", u.email, postID, root, err)
				continue
			}
			if resp.Msg.GetFail() {
				st.Errors++
				fmt.Printf("警告: %s 回复帖 %s 楼 %s 业务失败: code=%d msg=%s\n", u.email, postID, root, resp.Msg.GetErr().GetCode(), resp.Msg.GetErr().GetMsg())
				continue
			}
			st.Replies++
		}
	}

	return st
}
