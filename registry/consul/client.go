package consul

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"log"

	registry "github.com/kalandramo/bald/registry"

	"github.com/hashicorp/consul/api"
)

type Datacenter string

const (
	SingleDatacenter Datacenter = "SINGLE"
	MultiDatacenter  Datacenter = "MULTI"
)

// Client is consul client config
type Client struct {
	dc     Datacenter
	cli    *api.Client
	ctx    context.Context
	cancel context.CancelFunc

	// resolve service entry endpoints
	resolver ServiceResolver
	// healthcheck time interval in seconds
	healthcheckInterval int
	// healthCheckTimeout 是 TCP 检查的 timeout（进 register 请求体）
	healthCheckTimeout time.Duration
	// heartbeat enable heartbeat
	heartbeat bool
	// deregisterCriticalServiceAfter time interval in seconds
	deregisterCriticalServiceAfter int
	// serviceChecks  user custom checks
	serviceChecks api.AgentServiceChecks

	// hbMu 保护 heartbeats：每个已注册实例一条独立心跳，Deregister 只停
	// 该实例的心跳，不能取消所有实例共用的 ctx（否则注销一个实例会让其余
	// 实例停止续约并被 Consul 逐出）。
	hbMu       sync.Mutex
	heartbeats map[string]context.CancelFunc
}

func defaultResolver(_ context.Context, entries []*api.ServiceEntry) []*registry.ServiceInstance {
	services := make([]*registry.ServiceInstance, 0, len(entries))
	for _, entry := range entries {
		var version string
		for _, tag := range entry.Service.Tags {
			ss := strings.SplitN(tag, "=", 2)
			if len(ss) == 2 && ss[0] == "version" {
				version = ss[1]
			}
		}
		endpoints := make([]string, 0)
		for scheme, addr := range entry.Service.TaggedAddresses {
			if scheme == "lan_ipv4" || scheme == "wan_ipv4" || scheme == "lan_ipv6" || scheme == "wan_ipv6" {
				continue
			}
			endpoints = append(endpoints, addr.Address)
		}
		if len(endpoints) == 0 && entry.Service.Address != "" && entry.Service.Port != 0 {
			endpoints = append(endpoints, fmt.Sprintf("http://%s:%d", entry.Service.Address, entry.Service.Port))
		}
		services = append(services, &registry.ServiceInstance{
			ID:        entry.Service.ID,
			Name:      entry.Service.Service,
			Metadata:  entry.Service.Meta,
			Version:   version,
			Endpoints: endpoints,
		})
	}

	return services
}

// ServiceResolver is used to resolve service endpoints
type ServiceResolver func(ctx context.Context, entries []*api.ServiceEntry) []*registry.ServiceInstance

// Service get services from consul
func (c *Client) Service(ctx context.Context, service string, index uint64, passingOnly bool) ([]*registry.ServiceInstance, uint64, error) {
	if c.dc == MultiDatacenter {
		return c.multiDCService(ctx, service, index, passingOnly)
	}

	opts := &api.QueryOptions{
		WaitIndex:  index,
		WaitTime:   time.Second * 55,
		Datacenter: string(c.dc),
	}
	opts = opts.WithContext(ctx)

	if c.dc == SingleDatacenter {
		opts.Datacenter = ""
	}

	entries, meta, err := c.singleDCEntries(service, "", passingOnly, opts)
	if err != nil {
		return nil, 0, err
	}
	return c.resolver(ctx, entries), meta.LastIndex, nil
}

func (c *Client) multiDCService(ctx context.Context, service string, index uint64, passingOnly bool) ([]*registry.ServiceInstance, uint64, error) {
	opts := &api.QueryOptions{
		WaitIndex: index,
		WaitTime:  time.Second * 55,
	}
	opts = opts.WithContext(ctx)

	var instances []*registry.ServiceInstance

	dcs, err := c.cli.Catalog().Datacenters()
	if err != nil {
		return nil, 0, err
	}

	for _, dc := range dcs {
		opts.Datacenter = dc
		e, m, err := c.singleDCEntries(service, "", passingOnly, opts)
		if err != nil {
			return nil, 0, err
		}

		ins := c.resolver(ctx, e)
		for _, in := range ins {
			if in.Metadata == nil {
				in.Metadata = make(map[string]string, 1)
			}
			in.Metadata["dc"] = dc
		}

		instances = append(instances, ins...)
		opts.WaitIndex = m.LastIndex
	}

	return instances, opts.WaitIndex, nil
}

func (c *Client) singleDCEntries(service, tag string, passingOnly bool, opts *api.QueryOptions) ([]*api.ServiceEntry, *api.QueryMeta, error) {
	return c.cli.Health().Service(service, tag, passingOnly, opts)
}

// Register register service instance to consul
func (c *Client) Register(_ context.Context, svc *registry.ServiceInstance, enableHealthCheck bool) error {
	addresses := make(map[string]api.ServiceAddress, len(svc.Endpoints))
	checkAddresses := make([]string, 0, len(svc.Endpoints))
	for _, endpoint := range svc.Endpoints {
		raw, err := url.Parse(endpoint)
		if err != nil {
			return err
		}
		addr := raw.Hostname()
		port, _ := strconv.ParseUint(raw.Port(), 10, 16)

		checkAddresses = append(checkAddresses, net.JoinHostPort(addr, strconv.FormatUint(port, 10)))
		addresses[raw.Scheme] = api.ServiceAddress{Address: endpoint, Port: int(port)}
	}
	asr := &api.AgentServiceRegistration{
		ID:              svc.ID,
		Name:            svc.Name,
		Meta:            svc.Metadata,
		Tags:            []string{fmt.Sprintf("version=%s", svc.Version)},
		TaggedAddresses: addresses,
	}
	if len(checkAddresses) > 0 {
		host, portRaw, _ := net.SplitHostPort(checkAddresses[0])
		port, _ := strconv.ParseInt(portRaw, 10, 32)
		asr.Address = host
		asr.Port = int(port)
	}
	if enableHealthCheck {
		for _, address := range checkAddresses {
			asr.Checks = append(asr.Checks, &api.AgentServiceCheck{
				TCP:                            address,
				Interval:                       fmt.Sprintf("%ds", c.healthcheckInterval),
				DeregisterCriticalServiceAfter: fmt.Sprintf("%ds", c.deregisterCriticalServiceAfter),
				Timeout:                        c.healthCheckTimeout.String(),
			})
		}
		// custom checks
		asr.Checks = append(asr.Checks, c.serviceChecks...)
	}
	if c.heartbeat {
		asr.Checks = append(asr.Checks, &api.AgentServiceCheck{
			CheckID:                        "service:" + svc.ID,
			TTL:                            fmt.Sprintf("%ds", c.healthcheckInterval*2),
			DeregisterCriticalServiceAfter: fmt.Sprintf("%ds", c.deregisterCriticalServiceAfter),
		})
	}

	err := c.cli.Agent().ServiceRegister(asr)
	if err != nil {
		return err
	}
	if c.heartbeat {
		hbCtx, hbCancel := context.WithCancel(c.ctx)
		c.setHeartbeat(svc.ID, hbCancel)
		go func() {
			defer hbCancel()

			select {
			case <-hbCtx.Done():
				_ = c.cli.Agent().ServiceDeregister(svc.ID)
				return
			case <-time.After(time.Second):
			}
			if herr := c.cli.Agent().UpdateTTL("service:"+svc.ID, "pass", "pass"); herr != nil {
				log.Printf("[Consul] update ttl heartbeat to consul failed! err=%v", herr)
			}

			ticker := time.NewTicker(time.Second * time.Duration(c.healthcheckInterval))
			defer ticker.Stop()
			for {
				select {
				case <-hbCtx.Done():
					_ = c.cli.Agent().ServiceDeregister(svc.ID)
					return
				case <-ticker.C:
					herr := c.cli.Agent().UpdateTTL("service:"+svc.ID, "pass", "pass")
					if herr == nil {
						continue
					}
					log.Printf("[Consul] update ttl heartbeat to consul failed! err=%v", herr)
					// when the previous report fails, try to re register the service
					select {
					case <-hbCtx.Done():
						_ = c.cli.Agent().ServiceDeregister(svc.ID)
						return
					case <-time.After(time.Duration(rand.Intn(5)) * time.Second):
					}
					if rerr := c.cli.Agent().ServiceRegister(asr); rerr != nil {
						log.Printf("[Consul] re registry service failed! err=%v", rerr)
					} else {
						log.Printf("[Consul] re registry of service occurred success")
					}
				}
			}
		}()
	}
	return nil
}

// setHeartbeat 登记实例的独立心跳取消函数；同名实例重复注册时先停旧心跳。
func (c *Client) setHeartbeat(id string, cancel context.CancelFunc) {
	c.hbMu.Lock()
	defer c.hbMu.Unlock()
	if c.heartbeats == nil {
		c.heartbeats = make(map[string]context.CancelFunc)
	}
	if old := c.heartbeats[id]; old != nil {
		old()
	}
	c.heartbeats[id] = cancel
}

// takeHeartbeat 取出并移除实例的心跳取消函数。
func (c *Client) takeHeartbeat(id string) context.CancelFunc {
	c.hbMu.Lock()
	defer c.hbMu.Unlock()
	cancel := c.heartbeats[id]
	delete(c.heartbeats, id)
	return cancel
}

// Deregister service by service ID
func (c *Client) Deregister(_ context.Context, serviceID string) error {
	// 只停该实例的心跳：c.ctx 由所有实例共享，取消它会让其余服务停止续约。
	if cancel := c.takeHeartbeat(serviceID); cancel != nil {
		cancel()
	}
	return c.cli.Agent().ServiceDeregister(serviceID)
}
