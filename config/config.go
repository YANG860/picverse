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

// JWTConfig JWT 签发配置
type JWTConfig struct {
	// HMAC-SHA256 签名密钥，必填
	Secret string `mapstructure:"secret"`
	// 签发者，可选
	Issuer string `mapstructure:"issuer"`
	// token 有效期，默认 24h
	Expire time.Duration `mapstructure:"expire"`
}

type DatabaseConfig struct {
	PG struct {
		URL string `mapstructure:"url"`
	} `mapstructure:"pg"`
}

type SnowFlakeConfig struct {
	ID int64 `mapstructure:"id"`
}

type ServerConfig struct {
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
	Media MediaConsumerConfig `mapstructure:"media"`
}

type MediaConsumerConfig struct {
	Name       string `mapstructure:"name"`
	MaxRetry   int    `mapstructure:"max_retry"`
	WorkingDir string `mapstructure:"working_dir"`
}
