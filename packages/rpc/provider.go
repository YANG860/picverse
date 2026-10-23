package rpc

import (
	"context"
	"fmt"

	"rednote/config"
)

// ServiceName 服务逻辑名：调用方按名字解析地址，不感知具体实例
type ServiceName string

const (
	ServiceAuth  ServiceName = "auth"
	ServiceUser  ServiceName = "user"
	ServiceMedia ServiceName = "media"
	ServicePost  ServiceName = "post"
)

// EndpointProvider 服务地址解析接口。
// 返回地址池而非单地址：静态多实例场景池内轮询；k8s 场景池退化为 1 个
type EndpointProvider interface {
	// Resolve 返回 service 当前可用的地址池
	Resolve(ctx context.Context, service ServiceName) ([]string, error)
}

// StaticProvider EndpointProvider 的静态配置实现（初版）。
// 上 k8s 后由 Service DNS 实现替换（config 直接填 DNS 名，无需改代码）。
type StaticProvider struct {
	endpoints map[ServiceName][]string
}

// NewStaticProvider 从配置构造静态地址提供者。
// 四个下游服务地址均必填，缺失启动即失败（fail-fast，与 packages/redis、
// packages/oss 的配置校验风格一致，避免运行期才发现配置残缺）
func NewStaticProvider(cfg *config.Config) *StaticProvider {
	if cfg == nil {
		panic(fmt.Errorf("config is required"))
	}
	p := &StaticProvider{endpoints: map[ServiceName][]string{}}
	for svc, addr := range map[ServiceName]string{
		ServiceAuth:  cfg.Services.Auth,
		ServiceUser:  cfg.Services.User,
		ServiceMedia: cfg.Services.Media,
		ServicePost:  cfg.Services.Post,
	} {
		if addr == "" {
			panic(fmt.Errorf("services.%s is required", svc))
		}
		p.endpoints[svc] = []string{addr}
	}
	return p
}

// Resolve 返回服务的静态地址池
func (p *StaticProvider) Resolve(_ context.Context, service ServiceName) ([]string, error) {
	endpoints, ok := p.endpoints[service]
	if !ok || len(endpoints) == 0 {
		return nil, fmt.Errorf("unknown service: %s", service)
	}
	return endpoints, nil
}
