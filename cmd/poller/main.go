package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"rednote/cmd/gen"
	"rednote/config"
	"syscall"
)

func main() {

	var cfgPath string
	flag.StringVar(&cfgPath, "config", "./", "path to config file")
	flag.Parse()
	cfg := config.Init(cfgPath)

	// Outbox 轮询：待发消息 → NATS JetStream（事务性消息最终一致性的执行者）
	outboxPoller, cleanupOutbox := gen.InitializePoller(cfg)
	go outboxPoller.Start()

	// Redis 浏览计数刷回轮询：增量 → posts.view_count（PV 近似，只丢不重）
	redisPoller, cleanupRedis := gen.InitializeRedisPoller(cfg)
	pollerCtx, cancelPoller := context.WithCancel(context.Background())
	go redisPoller.Run(pollerCtx)

	// 收件箱保留期清理：过期行批量删（维护 fanout_timelines 表大小）
	fanoutCleaner, cleanupFanout := gen.InitializeFanoutCleaner(cfg)
	go fanoutCleaner.Run(pollerCtx)

	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGINT, syscall.SIGTERM)
	<-term

	cancelPoller() // RedisPoller 退出前自行冲刷残留增量
	outboxPoller.Close()
	cleanupFanout()
	cleanupRedis()
	cleanupOutbox()
}
