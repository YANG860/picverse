package defaults

const (
	PostMessageStream = "post"
	PostStream        = "post"

	// FavouriteCap 收藏夹容量上限：保证全量收藏一页可拉完（收藏态前端预取判态的前提）；
	// 仅新增收藏校验，已藏重复操作幂等不报满
	FavouriteCap = 1000
)
