package config

import "time"

var Global = &Config{}

type Config struct {
	SnowFlake SnowFlakeConfig `mapstructure:"snowflake"`
	Database  DatabaseConfig  `mapstructure:"database"`
	Server    ServerConfig    `mapstructure:"server"`
	S3        S3Config        `mapstructure:"s3"`
	JetStream JetStreamConfig `mapstructure:"jetstream"`
	Poller    PollerConfig    `mapstructure:"poller"`
	Consumer  ConsumerConfig  `mapstructure:"consumer"`
	Logger    LoggerConfig    `mapstructure:"logger"`
	JWT       JWTConfig       `mapstructure:"jwt"`
	Redis     RedisConfig     `mapstructure:"redis"`
	Meili     MeiliConfig     `mapstructure:"meili"`
	Cache     CacheConfig     `mapstructure:"cache"`
	Post      PostConfig      `mapstructure:"post"`
	BigV      BigVConfig      `mapstructure:"bigv"`
	Fanout    FanoutConfig    `mapstructure:"fanout"`
	Hot       HotConfig       `mapstructure:"hot"`
	Recommend RecommendConfig `mapstructure:"recommend"`
	Services  ServicesConfig  `mapstructure:"services"`
}

// ServicesConfig 下游服务地址（packages/discovery 静态实现的地址源）。
// 上 k8s 后改为 Service DNS 名（如 "post:8082"），接口与调用方零改动
type ServicesConfig struct {
	// 服务发现：各下游服务地址，host:port 或 scheme://host:port
	Auth     string `mapstructure:"auth"`
	User     string `mapstructure:"user"`
	Media    string `mapstructure:"media"`
	Post     string `mapstructure:"post"`
	Discover string `mapstructure:"discover"`
}

// CacheConfig 缓存层配置（packages/cache）
type CacheConfig struct {
	// Enabled 总开关：false 时注入 Noop（不连接 redis），行为与无缓存一致
	Enabled bool `mapstructure:"enabled"`
	// ProfileTTL 用户资料缓存 TTL（写入时附加 ±10% 抖动），0 使用默认 30m
	ProfileTTL time.Duration `mapstructure:"profile_ttl"`
	// PostTTL 帖子详情缓存 TTL（写入时附加 ±10% 抖动），0 使用默认 30m
	PostTTL time.Duration `mapstructure:"post_ttl"`
	// NullTTL 空哨兵 TTL（防穿透），0 使用默认 60s
	NullTTL time.Duration `mapstructure:"null_ttl"`
	// TimelineTTL 作者发件箱缓存 TTL（每作者近期帖窗口，pull 现拉加速层），0 使用默认 7d
	TimelineTTL time.Duration `mapstructure:"timeline_ttl"`
	// TimelineCapacity 发件箱缓存条数保险丝（防事故刷帖撑爆内存）；
	// 窗口语义 = fanout.retention 时间截尾（承诺窗口内全量，短取即见底免回源），正常永不触发，0 使用默认 5000
	TimelineCapacity int `mapstructure:"timeline_capacity"`
}

// PostConfig 帖子服务运行参数
type PostConfig struct {
	// ViewFlushInterval 浏览计数刷回周期（Redis → posts.view_count），0 使用默认 30s
	ViewFlushInterval time.Duration `mapstructure:"view_flush_interval"`
}

// BigVConfig 大V判定配置（发帖侧分流阈值，见 doc/internal/discover/discover.md）
type BigVConfig struct {
	// PromoteFollowers 大V晋升线（粉丝数 ≥ 该值写发件箱缓存走 pull，否则扇出收件箱），0 使用默认 5000
	PromoteFollowers int64 `mapstructure:"promote_followers"`
	// MarkTTL 大V标识 TTL（防抖动保持窗）：晋升后至少该时长维持大V供给路径，
	// 粉丝数抖动不切换供给线；跌破线后标识自然过期回归扇出，0 使用默认 7 天
	MarkTTL time.Duration `mapstructure:"mark_ttl"`
}

// FanoutConfig 收件箱（fanout_timelines）保留期清理配置（维护表大小，见 doc/internal/discover/discover.md）
type FanoutConfig struct {
	// Retention 收件箱保留期（按帖子时间 post_created_at，与游标同口径），0 使用默认 720h（30 天）
	Retention time.Duration `mapstructure:"retention"`
	// CleanupInterval 清理任务运行周期，0 使用默认 1h
	CleanupInterval time.Duration `mapstructure:"cleanup_interval"`
}

// HotConfig 热榜池（rn:v1:hot:posts）维护任务与热度分配置
// （见 doc/internal/discover/plan_recommend_hot.md）
type HotConfig struct {
	// RefreshInterval 维护周期（入池/算分/淘汰/TTL 续期），0 使用默认 10m
	RefreshInterval time.Duration `mapstructure:"refresh_interval"`
	// Window 候选时间窗（窗口外帖子自然出池），0 使用默认 168h（7 天）
	Window time.Duration `mapstructure:"window"`
	// TTL 池 key TTL 兜底（维护停摆消亡，随写入续期），0 使用默认 30m
	TTL time.Duration `mapstructure:"ttl"`
	// HardCap 池容量事故保险丝（候选超限按分数截尾），0 使用默认 500000
	HardCap int `mapstructure:"hard_cap"`
	// Gravity 热度分时间衰减陡度，0 使用默认 1.5
	Gravity float64 `mapstructure:"gravity"`
	// Weights 热度分信号权重；整组零值时使用默认组，单个 0 是合法权重
	Weights HotWeights `mapstructure:"weights"`
}

// HotWeights 热度分信号权重（engagement = Σ wᵢ·ln(1+countᵢ)）
type HotWeights struct {
	Like      float64 `mapstructure:"like"`
	Favourite float64 `mapstructure:"favourite"`
	Comment   float64 `mapstructure:"comment"`
	View      float64 `mapstructure:"view"`
}

// Zero 判定整组权重是否未配置（全零 → 调用方回退默认组）
func (w HotWeights) Zero() bool {
	return w.Like == 0 && w.Favourite == 0 && w.Comment == 0 && w.View == 0
}

// RecommendConfig 推荐流（GetRecommendFeed）配置（见 doc/internal/discover/plan_recommend_hot.md）
type RecommendConfig struct {
	// SeenTTL seen 已曝光去重窗口（账本 key TTL 随记账滚动续期），0 使用默认 720h（30 天）
	SeenTTL time.Duration `mapstructure:"seen_ttl"`
	// ScanBatchFactor 候选扫描批次 = limit × factor（预留过滤跳过），0 使用默认 3
	ScanBatchFactor int `mapstructure:"scan_batch_factor"`
	// ScatterWindow 同作者打散滑窗（窗内同作者 ≤ 1），0 使用默认 3
	ScatterWindow int `mapstructure:"scatter_window"`
	// ExploreRatio 探索位比例（每页随机采样条数占比，至少 1 条），0 使用默认 0.1
	ExploreRatio float64 `mapstructure:"explore_ratio"`
}

// RedisConfig Redis 连接配置
type RedisConfig struct {
	// 连接地址，如 "127.0.0.1:6379"
	Addr string `mapstructure:"addr"`
	// 密码，可为空
	Password string `mapstructure:"password"`
	// 库编号，默认 0
	DB int `mapstructure:"db"`
	// 连接池大小，0 表示使用默认值（10）
	PoolSize int `mapstructure:"pool_size"`
}

// MeiliConfig Meilisearch 搜索引擎配置（搜推服务的索引同步与查询）
type MeiliConfig struct {
	// 服务地址，如 "http://127.0.0.1:7700"
	URL string `mapstructure:"url"`
	// API 密钥（MEILI_MASTER_KEY），必填
	APIKey string `mapstructure:"api_key"`
}

// JWTConfig JWT 签发配置
type JWTConfig struct {
	// HMAC-SHA256 签名密钥，必填
	Secret string `mapstructure:"secret"`
	// 签发者，可选
	Issuer string `mapstructure:"issuer"`
	// token 有效期，默认 24h
	AccessExpire  time.Duration `mapstructure:"access_expire"`
	RefreshExpire time.Duration `mapstructure:"refresh_expire"`
}

type DatabaseConfig struct {
	PG struct {
		URL string `mapstructure:"url"`
	} `mapstructure:"pg"`
}

type SnowFlakeConfig struct {
	ID int64 `mapstructure:"id"`
}

// ServerConfig 各服务监听地址：多进程共用同一配置文件，各服务取自己的条目
type ServerConfig struct {
	Auth     string `mapstructure:"auth"`
	User     string `mapstructure:"user"`
	Media    string `mapstructure:"media"`
	Post     string `mapstructure:"post"`
	Bff      string `mapstructure:"bff"`
	Discover string `mapstructure:"discover"`
}

// S3Config S3/Minio 对象存储配置
type S3Config struct {
	Endpoint  string `mapstructure:"endpoint"`
	AccessKey string `mapstructure:"access_key"`
	SecretKey string `mapstructure:"secret_key"`
	Region    string `mapstructure:"region"`
	UseSSL    bool   `mapstructure:"use_ssl"`
	Bucket    string `mapstructure:"bucket"`
}

type LoggerConfig struct {
	Level string `mapstructure:"level"`

	// 启用文件输出
	File bool   `mapstructure:"file"`
	Out  string `mapstructure:"out"`
	// 启用轮转
	Rotate bool `mapstructure:"rotate"`

	// 轮转配置
	// 单个日志文件最大尺寸（MB），默认 100
	MaxSize int `mapstructure:"maxsize"`
	// 保留旧日志文件最大数量，默认 7
	MaxBackups int `mapstructure:"maxbackups"`
	// 保留旧日志文件最大天数，默认 28
	MaxAge int `mapstructure:"maxage"`
	// 是否压缩旧日志文件，默认 false
	Compress bool `mapstructure:"compress"`

	// 缓冲区大小（字节），默认 256KB。设为 0 禁用缓冲
	BufferSize int `mapstructure:"buffersize"`

	// 启用标准输出
	Std bool `mapstructure:"std"`
}

type JetStreamConfig struct {
	Url string `mapstructure:"url"`
}

type PollerConfig struct {
	Workers        int           `mapstructure:"workers"`
	Interval       time.Duration `mapstructure:"interval"`
	BatchSize      int           `mapstructure:"batch_size"`
	MaxRetry       int           `mapstructure:"max_retry"`
	PublishTimeout time.Duration `mapstructure:"publish_timeout"`
}

type ConsumerConfig struct {
	Media    MediaConsumerConfig    `mapstructure:"media"`
	Post     PostConsumerConfig     `mapstructure:"post"`
	Discover DiscoverConsumerConfig `mapstructure:"discover"`
}

type MediaConsumerConfig struct {
	Name       string `mapstructure:"name"`
	MaxRetry   int    `mapstructure:"max_retry"`
	WorkingDir string `mapstructure:"working_dir"`
}

type PostConsumerConfig struct {
	Name       string `mapstructure:"name"`
	MaxRetry   int    `mapstructure:"max_retry"`
	WorkingDir string `mapstructure:"working_dir"`
}

// DiscoverConsumerConfig 搜推消费者配置：索引同步与关注流扇出拆为独立 durable consumer
// （失败隔离：各自重试预算/死信/进度，见 doc/internal/discover/discover.md）
type DiscoverConsumerConfig struct {
	Index  DiscoverConsumerItem `mapstructure:"index"`
	Fanout DiscoverConsumerItem `mapstructure:"fanout"`
}

// DiscoverConsumerItem 单个搜推消费者的 durable 名与重试上限
type DiscoverConsumerItem struct {
	Name     string `mapstructure:"name"`
	MaxRetry int    `mapstructure:"max_retry"`
}
