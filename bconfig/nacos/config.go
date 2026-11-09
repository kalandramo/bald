package nacos

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

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

// newConfigClient 是建连接缝：真实 SDK 构造会起后台服务端列表轮询并写本地日志，
// 离线单测用桩替换后可以确定性覆盖建连成功/失败两条路径。
var newConfigClient = clients.NewConfigClient

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
	client, err := newConfigClient(vo.NacosClientParam{
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

// loadRetryAttempts 是 [Config.Load] 的查询上界。出处见 [loadRetryInterval]。
const loadRetryAttempts = 4

// loadRetryInterval 是重试间隔：250ms。
//
// 这个数不是拍的，钉的是 nacos-sdk-go 自己的**进程内**令牌桶
// （config_client/limiter.go:42-56：`rate.NewLimiter(5, 5)`，按
// `dataId@@group@@namespace` 分键、包级变量 ⇒ 同进程所有 client 共享）。超出桶的
// 查询**根本不发包**就被拒，错误串 "ConfigQueryRequest is limited"。
//
// 实测（v2.3.5 + nacos-server v2.3.2 standalone，本机，一次一进程）：
//   - 纯本地：连续调 IsLimited 同一个键，20 次里恰好放行第 0..4 次，之后全判限；
//   - 同一个 client 连续 60 次查询：54 次判限，首次落在第 6 次；每次「新建 client +
//     查询」重复 60 次也**全拒且耗时 0s**（零往返，拒绝在本地）——换 client 不换桶，
//     所以多应用共用一个 dataID 时必然互踩；
//   - 单个许可的回填：突发停住后每 50ms 重试，三轮分别在第 3/3/4 次成功，
//     即 **160ms / 170ms / 220ms**。
//
// 250ms 是实测上界 220ms 的余量，4 次约 1 秒覆盖一次完整回填周期。
// 变量而非常量：时序用例把它调小，不为测试新增公开旋钮。
var loadRetryInterval = 250 * time.Millisecond

// Load 实现 [bconfig.Reader]：返回 dataID（或默认值）下的原始配置内容。
//
// 带有限重试，因为 "is limited" 是**自我施加且会回填**的：一个进程里连续起多个
// 应用（装配测试、多角色示例）时，同一个 dataID 的查询会攒到桶外，第一次失败隔
// 一个回填周期就该成功。ctx 取消或到期立刻退出，不等下一次重试。
func (c *Config) Load(ctx context.Context, key string) ([]byte, error) {
	if err := c.init(); err != nil {
		return nil, err
	}
	dataID := c.resolveDataID(key)
	var lastErr error
	for attempt := 1; attempt <= loadRetryAttempts; attempt++ {
		if attempt > 1 {
			if err := sleepCtx(ctx, loadRetryInterval); err != nil {
				return nil, fmt.Errorf("nacos: load %q: %w", dataID, err)
			}
		}
		content, err := c.client.GetConfig(vo.ConfigParam{
			DataId: dataID,
			Group:  c.opts.group,
		})
		if err == nil {
			return []byte(content), nil
		}
		lastErr = err
	}
	return nil, loadFailed(dataID, lastErr)
}

// sleepCtx 等 d，期间 ctx 取消/到期就返回其错误。
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// loadFailed 把重试上界和**候选真因**都写进错误。
//
// 必须自带提示：SDK 在快照缺失时把拒绝原因换成了缓存文件的错误
// （config_client.go:200-215 —— 查询失败后读盘，读盘也失败就只把 readDiskErr 拼进
// 文案，服务端/本地桶的原因被丢弃、只在 SDK 日志里）。消费方拿到
// 「read config from both server and cache fail」是查不到真因的。
//
// 同一处还有第二条：默认（不禁快照）时读盘**成功**就返回陈旧快照且 err == nil，
// 限流就此静默变成「配置没变」。那条要动 DisableUseSnapShot，是删能力，未动。
func loadFailed(dataID string, err error) error {
	return fmt.Errorf("nacos: load %q failed after %d attempts: %w; a local rejection is also possible, since nacos-sdk-go allows only 5 config queries per second per data id in one process",
		dataID, loadRetryAttempts, err)
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
