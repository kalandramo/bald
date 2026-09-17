// Package nacos 提供 nacos 后端的服务注册/发现实现（Registrar + Discovery）。
//
// 双模式构造（对齐 bconfig provider 约定）：
//   - New(opts...)：按 options 自建 naming client 并拥有其生命周期
//     （Close 时关闭）——契约装配路径；
//   - NewWithClient(cli, opts...)：注入既有 INamingClient，不负责关闭。
//
// 契约装配走子包 contract。移植自 go-wind-plugins/registry/nacos，
// 实例类型换用 registry.ServiceInstance。
package nacos

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	v2vo "github.com/nacos-group/nacos-sdk-go/v2/vo"

	registry "github.com/kalandramo/bald/registry"
)

// ErrServiceInstanceNameEmpty 实例名为空。
var ErrServiceInstanceNameEmpty = errors.New("bald/nacos: ServiceInstance.Name can not be empty")

var (
	_ registry.Registrar = (*Registry)(nil)
	_ registry.Discovery = (*Registry)(nil)
)

// Registry 是 nacos 后端的注册中心实现。
type Registry struct {
	opts options
	cli  naming_client.INamingClient
}

// New 按 options 自建 nacos naming client 并构造 Registry（拥有 client 生命周期）。
func New(opts ...Option) (*Registry, error) {
	op := newOptions(opts...)
	if len(op.serverAddrs) == 0 {
		return nil, errors.New("nacos: server addrs is required")
	}
	// v2 SDK 的 ClientConfig 为字段直赋（无函数式选项）。
	clientConfig := constant.ClientConfig{
		NamespaceId:         op.namespace,
		TimeoutMs:           uint64(op.timeout.Milliseconds()),
		NotLoadCacheAtStart: true,
		Username:            op.username,
		Password:            op.password,
	}
	serverConfigs := make([]constant.ServerConfig, 0, len(op.serverAddrs))
	for _, addr := range op.serverAddrs {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("nacos: invalid server addr %q: %w", addr, err)
		}
		p, err := strconv.ParseUint(port, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("nacos: invalid server addr %q: %w", addr, err)
		}
		serverConfigs = append(serverConfigs, constant.ServerConfig{
			IpAddr: host,
			Port:   p,
		})
	}
	cli, err := clients.NewNamingClient(v2vo.NacosClientParam{
		ClientConfig:  &clientConfig,
		ServerConfigs: serverConfigs,
	})
	if err != nil {
		return nil, fmt.Errorf("nacos: create naming client: %w", err)
	}
	return &Registry{opts: op, cli: cli}, nil
}

// NewWithClient 注入既有 nacos naming client 构造 Registry（不负责 client 关闭）。
func NewWithClient(cli naming_client.INamingClient, opts ...Option) (*Registry, error) {
	if cli == nil {
		return nil, errors.New("nacos: naming client is nil")
	}
	op := newOptions(opts...)
	return &Registry{opts: op, cli: cli}, nil
}

// Close 释放 Registry 资源。nacos SDK v2 的 ephemeral 实例随连接断开自动
// 注销；SDK 未暴露 client 级 Close，这里仅保留占位语义（幂等 no-op）。
func (r *Registry) Close() error { return nil }

// Register 注册一个实例：按 endpoint 逐个注册（服务名带协议后缀 name.scheme）。
func (r *Registry) Register(_ context.Context, si *registry.ServiceInstance) error {
	if si.Name == "" {
		return ErrServiceInstanceNameEmpty
	}
	for _, endpoint := range si.Endpoints {
		u, err := url.Parse(endpoint)
		if err != nil {
			return err
		}
		host, port, err := net.SplitHostPort(u.Host)
		if err != nil {
			return err
		}
		p, err := strconv.Atoi(port)
		if err != nil {
			return err
		}
		weight := r.opts.weight
		var rmd map[string]string
		if si.Metadata == nil {
			rmd = map[string]string{
				"kind":    u.Scheme,
				"version": si.Version,
			}
		} else {
			rmd = make(map[string]string, len(si.Metadata)+2)
			for k, v := range si.Metadata {
				rmd[k] = v
			}
			rmd["kind"] = u.Scheme
			rmd["version"] = si.Version
			if w, ok := si.Metadata["weight"]; ok {
				weight, err = strconv.ParseFloat(w, 64)
				if err != nil {
					weight = r.opts.weight
				}
			}
		}
		_, e := r.cli.RegisterInstance(v2vo.RegisterInstanceParam{
			Ip:          host,
			Port:        uint64(p),
			ServiceName: si.Name + "." + u.Scheme,
			Weight:      weight,
			Enable:      true,
			Healthy:     true,
			Ephemeral:   true,
			Metadata:    rmd,
			ClusterName: r.opts.cluster,
			GroupName:   r.opts.group,
		})
		if e != nil {
			return fmt.Errorf("nacos: RegisterInstance %s failed: %w", endpoint, e)
		}
	}
	return nil
}

// Deregister 注销实例（appkit 优雅停机时先于 servers.Stop 调用）。
func (r *Registry) Deregister(_ context.Context, service *registry.ServiceInstance) error {
	for _, endpoint := range service.Endpoints {
		u, err := url.Parse(endpoint)
		if err != nil {
			return err
		}
		host, port, err := net.SplitHostPort(u.Host)
		if err != nil {
			return err
		}
		p, err := strconv.Atoi(port)
		if err != nil {
			return err
		}
		if _, err = r.cli.DeregisterInstance(v2vo.DeregisterInstanceParam{
			Ip:          host,
			Port:        uint64(p),
			ServiceName: service.Name + "." + u.Scheme,
			GroupName:   r.opts.group,
			Cluster:     r.opts.cluster,
			Ephemeral:   true,
		}); err != nil {
			return err
		}
	}
	return nil
}

// Watch 订阅某服务名的实例变更。
func (r *Registry) Watch(ctx context.Context, serviceName string) (registry.Watcher, error) {
	return newWatcher(ctx, r.cli, serviceName, r.opts.group, r.opts.kind, []string{r.opts.cluster})
}

// GetService 拉取服务名下的健康实例。
func (r *Registry) GetService(_ context.Context, serviceName string) ([]*registry.ServiceInstance, error) {
	res, err := r.cli.SelectInstances(v2vo.SelectInstancesParam{
		ServiceName: serviceName,
		GroupName:   r.opts.group,
		HealthyOnly: true,
	})
	if err != nil {
		return nil, err
	}
	items := make([]*registry.ServiceInstance, 0, len(res))
	for _, in := range res {
		kind := r.opts.kind
		weight := r.opts.weight
		if k, ok := in.Metadata["kind"]; ok {
			kind = k
		}
		if in.Weight > 0 {
			weight = in.Weight
		}

		item := &registry.ServiceInstance{
			ID:        in.InstanceId,
			Name:      in.ServiceName,
			Version:   in.Metadata["version"],
			Metadata:  in.Metadata,
			Endpoints: []string{fmt.Sprintf("%s://%s:%d", kind, in.Ip, in.Port)},
		}
		if item.Metadata == nil {
			item.Metadata = map[string]string{}
		}
		item.Metadata["weight"] = strconv.Itoa(int(math.Ceil(weight)))
		items = append(items, item)
	}
	return items, nil
}
