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
	"connectrpc.com/grpcreflect"
	"connectrpc.com/validate"
	"rednote/gen/go/bff/v1/bffv1connect"
	"rednote/packages/trace"
)

func main() {

	var cfgPath string
	flag.StringVar(&cfgPath, "config", "./", "path to config file")
	flag.Parse()
	cfg := config.Init(cfgPath)

	svc, cleanup := gen.InitializeBFFServiceHandler(cfg)
	// BFF 面客唯一入口：trace → jwt（token 存在才注入 claims，认证强制由业务层判定，
	// Register/Login 无会话可过）→ 参数校验
	path, handler := bffv1connect.NewBFFServiceHandler(
		svc,
		connect.WithInterceptors(
			trace.NewServerInterceptor(),
			gen.InitializeAuthInterceptor(cfg),
			validate.NewInterceptor(),
		),
	)

	mux := http.NewServeMux()
	mux.Handle(path, handler)

	// gRPC 服务端反射（调试开关）：grpcurl / grpcui / buf curl 等工具免 schema 直查。
	// 仅 reflection: true 时挂载——反射暴露服务/消息元数据，生产面客端口应关闭；
	// 同时启用 h2c（明文 HTTP/2），grpcurl -plaintext 依赖它（HTTP/1.1 客户端不受影响）
	if cfg.Server.Reflection {
		reflector := grpcreflect.NewStaticReflector("bff.v1.BFFService")
		mux.Handle(grpcreflect.NewHandlerV1(reflector))
		mux.Handle(grpcreflect.NewHandlerV1Alpha(reflector))
	}

	srv := &http.Server{
		Addr: cfg.Server.Bff,
		// 明文 HTTP/2（h2c）：grpcurl -plaintext / buf curl --http2-prior-knowledge 依赖；
		// http.Protocols 为 Go 1.24+ 标准姿势（h2c 包已 deprecated），HTTP/1.1 客户端不受影响
		Protocols: new(http.Protocols),
	}
	srv.Protocols.SetHTTP1(true)
	srv.Protocols.SetUnencryptedHTTP2(true)
	srv.Handler = mux

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
