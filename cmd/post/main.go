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
	"rednote/gen/go/post/v1/postv1connect"
	"rednote/packages/trace"
)

func main() {

	var cfgPath string
	flag.StringVar(&cfgPath, "config", "./", "path to config file")
	flag.Parse()
	cfg := config.Init(cfgPath)

	svc, cleanup := gen.InitializePostServiceHandler(cfg)
	path, handler := postv1connect.NewPostServiceHandler(
		svc,
		connect.WithInterceptors(trace.NewServerInterceptor()),
	)

	mux := http.NewServeMux()
	mux.Handle(path, handler)

	srv := &http.Server{
		Addr:    ":8082",
		Handler: mux,
	}

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

	cleanup()
	log.Println("服务已优雅关闭")
}
