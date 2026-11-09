package defaults

// 大V判定默认值（config.bigv 对应项为 0 时回退），见 doc/internal/discover/discover.md
const (
	// BigVPromoteFollowers 大V晋升线（粉丝数 ≥ 该值写发件箱缓存走 pull，否则扇出收件箱）
	BigVPromoteFollowers = 5000
)
