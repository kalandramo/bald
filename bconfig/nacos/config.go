package nacos

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"

	"github.com/kalandramo/bald/bconfig"
)

var (
	_ bconfig.Reader       = (*Config)(nil)
	_ bconfig.ValueWatcher = (*Config)(nil)
)

// DefaultPort nacos 服务默认端口。
const DefaultPort = 8848

// Config 是 nacos 配置源。
//
// 双模式构造：
//   - [New]：从 options 自建 client（契约装配路径），首次 Load/WatchValue 惰性建连；
//   - [NewWithClient]：注入已有 client，本源不负责关闭（nacos-sdk-go 无显式关闭接口）。
type Config struct {
	opts   options
	client config_client.IConfigClient
}

// New 创建自建模式的 nacos 配置源（serverAddrs/dataID 必填）。
func New(opts ...Option) (*Config, error) {
	o := options{}
	for _, opt := range opts {
		opt(&o)
	}
	if len(o.serverAddrs) == 0 {
		return nil, errors.New("nacos: server addrs is required")
	}
	if o.dataID == "" {
		return nil, errors.New("nacos: data id is required")
	}
	return &Config{opts: o}, nil
}

// NewWithClient 创建注入模式的 nacos 配置源（dataID 必填）。
func NewWithClient(c config_client.IConfigClient, opts ...Option) (*Config, error) {
	if c == nil {
		return nil, errors.New("nacos: client is nil")
	}
	o := options{}
	for _, opt := range opts {
		opt(&o)
	}
	if o.dataID == "" {
		return nil, errors.New("nacos: data id is required")
	}
	return &Config{opts: o, client: c}, nil
}

// PublicNamespace 是 nacos 默认命名空间的**显示名**（控制台与文档都这么写）。
const PublicNamespace = "public"

// namespaceID 把契约/用户写的命名空间折成 SDK 认的 NamespaceId。
//
// 只有 "public" 需要折：默认命名空间在 HTTP 参数（tenant）里必须是空串。
// 实测（nacos-server v2.3.2 standalone，dataID 存在且可读）：
// NamespaceId="public" → GetConfig 返回**空内容且不报错**，也就是配置源安静地
// 贡献零个键；同一份内容用 NamespaceId="" → 拿到全文。危害形状正是最难查的那
// 种——远端配置源接上了、启动不报错、值一个也没进来。其余取值原样透传（自建
// namespace 的 ID 是用户自己起的字符串）。
func namespaceID(ns string) string {
	if ns == PublicNamespace {
		return ""
	}
	return ns
}

// clientConfig 组装 SDK 客户端参数。命名空间单独走 [namespaceID]——它是这条
// 路径上唯一会被写错的字段，所以拆成可测的一层，而不是埋在 init 里。
func clientConfig(o options) constant.ClientConfig {
	return constant.ClientConfig{
		NamespaceId: namespaceID(o.namespace),
		TimeoutMs:   5000,
		LogLevel:    "warn",
		Username:    o.username,
		Password:    o.password,
	}
}

// init 惰性建连（仅自建模式）。
func (c *Config) init() error {
	if c.client != nil {
		return nil
	}
	sc := make([]constant.ServerConfig, 0, len(c.opts.serverAddrs))
	for _, addr := range c.opts.serverAddrs {
		host, port := addr, DefaultPort
		if h, p, err := net.SplitHostPort(addr); err == nil {
			host, port = h, 0
			if pv, err := strconv.Atoi(p); err == nil {
				port = pv
			}
		}
		sc = append(sc, constant.ServerConfig{IpAddr: host, Port: uint64(port)})
	}
	cc := clientConfig(c.opts)
	client, err := clients.NewConfigClient(vo.NacosClientParam{
		ClientConfig:  &cc,
		ServerConfigs: sc,
	})
	if err != nil {
		return fmt.Errorf("nacos: new client: %w", err)
	}
	c.client = client
	return nil
}

// resolveDataID 返回实际读取的 dataID：key 非空时覆盖配置的默认值。
func (c *Config) resolveDataID(key string) string {
	if key != "" {
		return key
	}
	return c.opts.dataID
}

// Load 实现 [bconfig.Reader]：返回 dataID（或默认值）下的原始配置内容。
func (c *Config) Load(ctx context.Context, key string) ([]byte, error) {
	if err := c.init(); err != nil {
		return nil, err
	}
	dataID := c.resolveDataID(key)
	content, err := c.client.GetConfig(vo.ConfigParam{
		DataId: dataID,
		Group:  c.opts.group,
	})
	if err != nil {
		return nil, err
	}
	return []byte(content), nil
}

// WatchValue 实现 [bconfig.ValueWatcher]：dataID（或默认值）配置变更时推送
// 新内容；ctx 取消后反注册监听并关闭通道。
func (c *Config) WatchValue(ctx context.Context, key string) (<-chan []byte, error) {
	if err := c.init(); err != nil {
		return nil, err
	}
	dataID := c.resolveDataID(key)

	out := make(chan []byte, 1)

	err := c.client.ListenConfig(vo.ConfigParam{
		DataId: dataID,
		Group:  c.opts.group,
		OnChange: func(_, group, dID, data string) {
			if dID == dataID && group == c.opts.group {
				select {
				case out <- []byte(data):
				case <-ctx.Done():
				}
			}
		},
	})
	if err != nil {
		return nil, err
	}

	go func() {
		defer close(out)
		<-ctx.Done()
		_ = c.client.CancelListenConfig(vo.ConfigParam{
			DataId: dataID,
			Group:  c.opts.group,
		})
	}()

	return out, nil
}
