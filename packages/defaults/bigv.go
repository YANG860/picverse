package defaults

import "time"

// 大V判定默认值（config.bigv 对应项为 0 时回退），见 doc/internal/discover/discover.md
const (
	// BigVPromoteFollowers 大V晋升线（粉丝数 ≥ 该值写发件箱缓存走 pull，否则扇出收件箱）
	BigVPromoteFollowers = 5000
	// BigVMarkTTL 大V标识 TTL 默认值（防抖动保持窗）：晋升后至少该时长维持大V供给路径，
	// 粉丝数在晋升线附近抖动不切换供给线（发帖缓存连续）；跌破线后标识自然过期回归扇出
	BigVMarkTTL = 7 * 24 * time.Hour
)
