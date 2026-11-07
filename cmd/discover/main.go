package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"rednote/cmd/gen"
	"rednote/config"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/validate"
	"rednote/gen/go/discover/v1/discoverv1connect"
	"rednote/packages/trace"
)

func main() {

	var cfgPath string
	flag.StringVar(&cfgPath, "config", "./", "path to config file")
	flag.Parse()
	cfg := config.Init(cfgPath)

	svc, cleanup := gen.InitializeDiscoverServiceHandler(cfg)
	path, handler := discoverv1connect.NewDiscoverServiceHandler(
		svc,
		connect.WithInterceptors(trace.NewServerInterceptor(), validate.NewInterceptor()),
	)

	// 索引同步消费者：消费 post 流消息维护 Meili posts 索引（启动即校验索引/连接，fail-fast）
	discoverConsumer, consumerCleanup := gen.InitializeDiscoverConsumer(cfg)
	discoverConsumer.Start()

	mux := http.NewServeMux()
	mux.Handle(path, handler)

	srv := &http.Server{
		Addr:    cfg.Server.Discover,
		Handler: mux,
	}

	// TODO(P2): 热度快照定时任务启动

	// 启动服务
	go func() {
		log.Printf("服务启动，监听端口 %s", srv.Addr)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("服务异常退出: %v", err)
		}
		log.Println("服务已停止接受新请求")
	}()

	// 等待终止信号
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGINT, syscall.SIGTERM)
	sig := <-term
	log.Printf("收到信号 %v，开始优雅关闭...", sig)

	// 优雅关闭：等待现有请求处理完成，最多 10 秒
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("优雅关闭失败: %v", err)
	}

	discoverConsumer.Close()
	consumerCleanup()
	cleanup()
	log.Println("服务已优雅关闭")
}
