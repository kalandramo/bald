package consul

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/consul/api"

	registry "github.com/kalandramo/bald/registry"
)

var (
	_ registry.Registrar = (*Registry)(nil)
	_ registry.Discovery = (*Registry)(nil)
)

// Config is consul registry config
type Config struct {
	*api.Config
}

// Registry is consul registry
type Registry struct {
	cli               *Client
	enableHealthCheck bool
	registry          map[string]*serviceSet
	lock              sync.RWMutex
	timeout           time.Duration

	// 自建 client 模式参数（New 时消费，注入模式忽略）
	address string
	scheme  string
	token   string
}

// New 按 options 自建 consul client 并构造 Registry（拥有连接生命周期，
// Close 停心跳）。address/scheme/token 均空时走 api.DefaultConfig()
// （读 CONSUL_HTTP_ADDR 等环境变量）。
func New(opts ...Option) (*Registry, error) {
	r := newRegistry(nil, opts...)
	cfg := api.DefaultConfig()
	if r.address != "" {
		cfg.Address = r.address
	}
	if r.scheme != "" {
		cfg.Scheme = r.scheme
	}
	if r.token != "" {
		cfg.Token = r.token
	}
	c, err := api.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("consul: create client: %w", err)
	}
	r.cli.cli = c
	return r, nil
}

// NewWithClient 注入既有 consul client 构造 Registry（不负责关闭）。
func NewWithClient(apiClient *api.Client, opts ...Option) *Registry {
	return newRegistry(apiClient, opts...)
}

func newRegistry(apiClient *api.Client, opts ...Option) *Registry {
	r := &Registry{
		registry:          make(map[string]*serviceSet),
		enableHealthCheck: true,
		timeout:           10 * time.Second,
		cli: &Client{
			dc:                             SingleDatacenter,
			cli:                            apiClient,
			resolver:                       defaultResolver,
			healthcheckInterval:            10,
			heartbeat:                      true,
			deregisterCriticalServiceAfter: 600,
		},
	}
	for _, o := range opts {
		o(r)
	}
	r.cli.ctx, r.cli.cancel = context.WithCancel(context.Background())
	return r
}

// Close 停掉心跳/订阅级 ctx（consul SDK 的 api.Client 无需关闭）。
func (r *Registry) Close() error {
	if r.cli != nil && r.cli.cancel != nil {
		r.cli.cancel()
	}
	return nil
}

// Register register service
func (r *Registry) Register(ctx context.Context, svc *registry.ServiceInstance) error {
	return r.cli.Register(ctx, svc, r.enableHealthCheck)
}

// Deregister deregister service
func (r *Registry) Deregister(ctx context.Context, svc *registry.ServiceInstance) error {
	return r.cli.Deregister(ctx, svc.ID)
}

// GetService return service by name
func (r *Registry) GetService(ctx context.Context, name string) ([]*registry.ServiceInstance, error) {
	r.lock.RLock()
	defer r.lock.RUnlock()
	set := r.registry[name]

	getRemote := func() []*registry.ServiceInstance {
		services, _, err := r.cli.Service(ctx, name, 0, true)
		if err == nil && len(services) > 0 {
			return services
		}
		return nil
	}

	if set == nil {
		if s := getRemote(); len(s) > 0 {
			return s, nil
		}
		return nil, fmt.Errorf("service %s not resolved in registry", name)
	}
	ss, _ := set.services.Load().([]*registry.ServiceInstance)
	if ss == nil {
		if s := getRemote(); len(s) > 0 {
			return s, nil
		}
		return nil, fmt.Errorf("service %s not found in registry", name)
	}
	return ss, nil
}

// ListServices return service list.
func (r *Registry) ListServices() (allServices map[string][]*registry.ServiceInstance, err error) {
	r.lock.RLock()
	defer r.lock.RUnlock()
	allServices = make(map[string][]*registry.ServiceInstance)
	for name, set := range r.registry {
		var services []*registry.ServiceInstance
		ss, _ := set.services.Load().([]*registry.ServiceInstance)
		if ss == nil {
			continue
		}
		services = append(services, ss...)
		allServices[name] = services
	}
	return
}

// Watch resolve service by name
func (r *Registry) Watch(ctx context.Context, name string) (registry.Watcher, error) {
	r.lock.Lock()
	defer r.lock.Unlock()
	set, ok := r.registry[name]
	if !ok {
		set = &serviceSet{
			watcher:     make(map[*watcher]struct{}),
			services:    &atomic.Value{},
			serviceName: name,
		}
		r.registry[name] = set
	}

	// init watcher
	w := &watcher{
		event: make(chan struct{}, 1),
	}
	w.ctx, w.cancel = context.WithCancel(ctx)
	w.set = set
	set.lock.Lock()
	set.watcher[w] = struct{}{}
	set.lock.Unlock()
	ss, _ := set.services.Load().([]*registry.ServiceInstance)
	if len(ss) > 0 {
		// If the service has a value, it needs to be pushed to the watcher,
		// otherwise the initial data may be blocked forever during the watch.
		w.event <- struct{}{}
	}

	if !ok {
		err := r.resolve(ctx, set)
		if err != nil {
			return nil, err
		}
	}
	return w, nil
}

func (r *Registry) resolve(ctx context.Context, ss *serviceSet) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	services, idx, err := r.cli.Service(timeoutCtx, ss.serviceName, 0, true)
	if err != nil {
		return err
	}
	if len(services) > 0 {
		ss.broadcast(services)
	}

	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				timeoutCtx, cancel := context.WithTimeout(context.Background(), r.timeout)
				tmpService, tmpIdx, err := r.cli.Service(timeoutCtx, ss.serviceName, idx, true)
				cancel()
				if err != nil {
					time.Sleep(time.Second)
					continue
				}
				if len(tmpService) != 0 && tmpIdx != idx {
					services = tmpService
					ss.broadcast(services)
				}
				idx = tmpIdx
			case <-ctx.Done():
				return
			}
		}
	}()

	return nil
}
