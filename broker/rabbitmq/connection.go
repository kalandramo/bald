package rabbitmq

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kalandramo/bald/broker"
	amqp "github.com/rabbitmq/amqp091-go"
)

var (
	DefaultRabbitURL             = "amqp://guest:guest@127.0.0.1:5672"
	DefaultRequeueOnError        = false
	EnableLazyInitPublishChannel = true

	DefaultAmqpConfig = amqp.Config{
		Heartbeat: 10 * time.Second,
		Locale:    "en_US",
	}
)

type Exchange struct {
	Name    string
	Type    string // "direct", "fanout", "topic", "headers"
	Durable bool
}

var (
	DefaultExchange = Exchange{
		Name:    "amq.topic",
		Type:    "topic",
		Durable: true,
	}
)

type Qos struct {
	PrefetchCount  int
	PrefetchSize   int
	PrefetchGlobal bool
}

var (
	DefaultQos = Qos{
		PrefetchCount:  0,
		PrefetchSize:   0,
		PrefetchGlobal: false,
	}
)

type rabbitConnection struct {
	sync.Mutex

	Connection      *amqp.Connection
	Channel         *rabbitChannel
	ExchangeChannel *rabbitChannel

	options broker.Options

	url                 string
	exchanges           map[string]*Exchange
	defaultExchangeName string
	qos                 Qos

	confirmMode bool
	onReturn    ReturnHandler
	onConfirm   ConfirmHandler

	connected      bool
	close          chan bool
	waitConnection chan struct{}
}

func newRabbitMQConnection(opts broker.Options) *rabbitConnection {
	conn := &rabbitConnection{
		options:        opts,
		url:            DefaultRabbitURL,
		qos:            DefaultQos,
		close:          make(chan bool),
		waitConnection: make(chan struct{}),
	}

	conn.init()

	return conn
}

func (r *rabbitConnection) init() {
	if len(r.options.Addrs) > 0 && hasUrlPrefix(r.options.Addrs[0]) {
		r.url = r.options.Addrs[0]
	}

	r.exchanges = make(map[string]*Exchange)

	// start with default exchange
	defaultEx := DefaultExchange
	r.exchanges[defaultEx.Name] = &defaultEx
	r.defaultExchangeName = defaultEx.Name

	// apply legacy single-exchange options (override default exchange)
	legacyEx := defaultEx
	if val, ok := r.options.Context.Value(exchangeNameKey{}).(string); ok {
		legacyEx.Name = val
	}
	if val, ok := r.options.Context.Value(exchangeKindKey{}).(string); ok {
		legacyEx.Type = val
	}
	if val, ok := r.options.Context.Value(exchangeDurableKey{}).(bool); ok {
		legacyEx.Durable = val
	}
	if legacyEx.Name != defaultEx.Name {
		delete(r.exchanges, defaultEx.Name)
	}
	r.exchanges[legacyEx.Name] = &legacyEx
	r.defaultExchangeName = legacyEx.Name

	// apply multi-exchange registration
	if vals, ok := r.options.Context.Value(exchangesKey{}).([]Exchange); ok && len(vals) > 0 {
		for i := range vals {
			ex := vals[i]
			r.exchanges[ex.Name] = &ex
		}
		// if no legacy exchange name was set, use first as default
		if _, hasLegacy := r.options.Context.Value(exchangeNameKey{}).(string); !hasLegacy {
			r.defaultExchangeName = vals[0].Name
		}
	}

	// apply explicit default exchange name override
	if val, ok := r.options.Context.Value(defaultExchangeNameKey{}).(string); ok {
		if _, exists := r.exchanges[val]; exists {
			r.defaultExchangeName = val
		}
	}

	if val, ok := r.options.Context.Value(prefetchCountKey{}).(int); ok {
		r.qos.PrefetchCount = val
	}
	if val, ok := r.options.Context.Value(prefetchSizeKey{}).(int); ok {
		r.qos.PrefetchSize = val
	}
	if val, ok := r.options.Context.Value(prefetchGlobalKey{}).(bool); ok {
		r.qos.PrefetchGlobal = val
	}

	if val, ok := r.options.Context.Value(confirmModeKey{}).(bool); ok {
		r.confirmMode = val
	}
	if val, ok := r.options.Context.Value(onReturnKey{}).(ReturnHandler); ok {
		r.onReturn = val
	}
	if val, ok := r.options.Context.Value(onConfirmKey{}).(ConfirmHandler); ok {
		r.onConfirm = val
	}
}

func (r *rabbitConnection) connect(secure bool, config *amqp.Config) error {
	if err := r.tryConnect(secure, config); err != nil {
		return err
	}

	r.Lock()
	r.connected = true
	r.ExchangeChannel = nil
	r.signalConnectedLocked()
	r.Unlock()

	go r.reconnect(secure, config)
	return nil
}

// signalConnectedLocked 广播「连接已就绪」。waitConnection 是电平锁存而非边沿
// 广播：connected 期间通道保持关闭，晚到的等待方（正常 Start 之后才起
// resubscribe 的订阅者）必须立即放行——只唤醒「当下正阻塞者」的边沿语义会让
// 它们阻塞到下一次断线重连，连接健康期间零消费。close 用 select-default 判重：
// 并发 Connect 的第二次 signal、或上一轮遗留的关闭态直接跳过（修复前这里
// 裸 close，Connect→Close→Connect 是 close of closed channel panic）。
// 调用方必须已持有 r.Lock。
func (r *rabbitConnection) signalConnectedLocked() {
	select {
	case <-r.waitConnection:
		// 已是关闭态（锁存中）：无需重复 close。
	default:
		close(r.waitConnection)
	}
}

// waitChan 返回等待通道的快照。字段会被 connect/reconnect/Close 持锁置换，
// 等待方解锁后直接读字段与写端构成数据竞争；select 前必须经这里取快照。
func (r *rabbitConnection) waitChan() <-chan struct{} {
	r.Lock()
	ch := r.waitConnection
	r.Unlock()
	return ch
}

// isConnected 读连接就绪标志。connected 的写端在 connection 内部持锁，
// 跨锁裸读（此前订阅者借 broker 的 mtx 读它）构成数据竞争。
func (r *rabbitConnection) isConnected() bool {
	r.Lock()
	connected := r.connected
	r.Unlock()
	return connected
}

func (r *rabbitConnection) reconnect(secure bool, config *amqp.Config) {
	var connect bool
	reconnectDelay := 1 * time.Second
	maxReconnectDelay := 30 * time.Second

	for {
		if connect {
			if err := r.tryConnect(secure, config); err != nil {
				// AR-30：退避等待随 close 中断——Close 后不再无限重拨。
				select {
				case <-r.close:
					return
				case <-time.After(reconnectDelay):
				}
				if reconnectDelay < maxReconnectDelay {
					reconnectDelay *= 2
				}
				continue
			}

			r.Lock()
			r.connected = true
			r.ExchangeChannel = nil
			r.signalConnectedLocked()
			r.Unlock()
		}

		connect = true
		notifyClose := make(chan *amqp.Error)
		// 持锁快照连接再注册断线通知：并发 Connect 的 tryConnect 落锁换新
		// 连接时，这里不能无锁读字段。
		r.Lock()
		conn := r.Connection
		r.Unlock()
		if conn != nil {
			conn.NotifyClose(notifyClose)
		}

		select {
		case err := <-notifyClose:
			logError(err)
			r.Lock()
			r.connected = false
			r.waitConnection = make(chan struct{})
			r.ExchangeChannel = nil
			r.Unlock()
		case <-r.close:
			return
		}
	}
}

func (r *rabbitConnection) Connect(secure bool, config *amqp.Config) error {
	r.Lock()

	if r.connected {
		r.Unlock()
		return nil
	}

	select {
	case <-r.close:
		r.close = make(chan bool)
	default:
	}

	r.Unlock()

	return r.connect(secure, config)
}

func (r *rabbitConnection) Close() error {
	r.Lock()
	defer r.Unlock()

	select {
	case <-r.close:
		return nil
	default:
		close(r.close)
		r.connected = false
		// waitConnection 保持原样（与既有语义一致）：连接期间它是锁存的关闭态，
		// 被唤醒的等待方经 isConnected 复核；断线窗口里 notifyClose 已置换成
		// 开启态通道。下一次 Connect 的 signalConnectedLocked 以判重兼容两种状态。
	}

	if r.Connection == nil {
		return errors.New("connection is nil")
	}

	return r.Connection.Close()
}

// hostOf 从 AMQP URL 提取 host（TLS SNI 用）。解析失败返回空串
// （crypto/tls 在 ServerName 为空时按连接地址推导，仍走标准校验）。
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if host, _, err := net.SplitHostPort(u.Host); err == nil {
		return host
	}
	return u.Host
}

func (r *rabbitConnection) tryConnect(secure bool, config *amqp.Config) error {
	if config == nil {
		// 拷贝再改：TLS 分支会往 config 里写 TLSClientConfig，直接引用包级
		// DefaultAmqpConfig 会把 InsecureSkipVerify 永久写进全局默认值。
		cfgCopy := DefaultAmqpConfig
		config = &cfgCopy
	}

	url := r.url

	if secure || config.TLSClientConfig != nil || strings.HasPrefix(r.url, "amqps://") {
		if config.TLSClientConfig == nil {
			// AR-9：默认走标准证书校验——此前 InsecureSkipVerify: true 让
			// amqps:// 沦为明文级防护（中间人可截获契约注入 URL 的凭证与
			// 消息正文）。SNI 取 URL host；跳过校验等应由契约显式声明
			// （broker.rabbitmq 的 tls 段，见 architecture-review-20261118 AR-9）。
			config.TLSClientConfig = &tls.Config{ServerName: hostOf(r.url)}
		}

		url = strings.Replace(r.url, "amqp://", "amqps://", 1)
	}

	var err error
	conn, err := amqp.DialConfig(url, *config)
	if err != nil {
		return err
	}

	ch, chErr := newRabbitChannel(conn, r.qos)

	// 拨号/建通道是 I/O，不持锁进行；字段在此一次性落锁写入——此前
	// r.Connection/r.Channel 在锁外裸写，与 Consume、lazyInitPublishChannel
	// 的持锁读构成数据竞争（重连风暴窗口内尤其明显）。
	r.Lock()
	r.Connection = conn
	r.Channel = ch // 建通道失败时为 nil——与原实现一致，由错误返回方处理
	r.Unlock()

	if chErr != nil {
		return chErr
	}

	// declare all registered exchanges
	for _, ex := range r.exchanges {
		if err := ch.DeclareExchange(ex.Name, ex.Type, ex.Durable, false); err != nil {
			logErrorf("declare exchange '%s' failed: %v", ex.Name, err)
		}
	}

	if !EnableLazyInitPublishChannel {
		pubCh, pubErr := newRabbitChannel(conn, r.qos)
		if pubErr != nil {
			// 与既有语义对齐：失败时归 nil，下一次 lazyInit/重连重建，
			// 而不是留下指向旧连接的死通道。
			r.Lock()
			r.ExchangeChannel = nil
			r.Unlock()
			return pubErr
		}
		r.Lock()
		r.ExchangeChannel = pubCh
		r.Unlock()
	}

	return nil
}

func (r *rabbitConnection) GetExchange(name string) (*Exchange, error) {
	if name == "" {
		name = r.defaultExchangeName
	}
	ex, ok := r.exchanges[name]
	if !ok {
		return nil, fmt.Errorf("exchange '%s' not registered", name)
	}
	return ex, nil
}

func (r *rabbitConnection) Consume(exchangeName, queueName, routingKey string, bindArgs amqp.Table, qArgs amqp.Table, autoAck, durableQueue, autoDel bool) (*rabbitChannel, <-chan amqp.Delivery, error) {
	ex, err := r.GetExchange(exchangeName)
	if err != nil {
		return nil, nil, err
	}

	r.Lock()
	conn := r.Connection
	r.Unlock()

	// 与 lazyInitPublishChannel 同源的根因：半连接态（Connect 拨号失败后
	// wrapper 已建但底层 amqp.Connection 为 nil）下 conn 为 nil，若直接
	// newRabbitChannel(nil) 会在 (*amqp.Connection).Channel() 处 panic。
	// 发布侧已 fail-fast，这里对消费侧做对偶收口。
	if conn == nil {
		return nil, nil, fmt.Errorf("%w: underlying AMQP connection is nil (Connect failed or never established)", ErrNotConnected)
	}

	consumerChannel, err := newRabbitChannel(conn, r.qos)
	if err != nil {
		return nil, nil, err
	}

	if err = consumerChannel.DeclareExchange(ex.Name, ex.Type, ex.Durable, false); err != nil {
		return nil, nil, err
	}

	if err = consumerChannel.DeclareQueue(queueName, qArgs, durableQueue, autoDel); err != nil {
		return nil, nil, err
	}

	deliveries, err := consumerChannel.ConsumeQueue(queueName, autoAck)
	if err != nil {
		return nil, nil, err
	}

	if err = consumerChannel.BindQueue(queueName, routingKey, ex.Name, bindArgs); err != nil {
		return nil, nil, err
	}

	return consumerChannel, deliveries, nil
}

func (r *rabbitConnection) DeclarePublishQueue(exchangeName, queueName, routingKey string, bindArgs amqp.Table, queueArgs amqp.Table, durableQueue, autoDel bool) error {
	if err := r.lazyInitPublishChannel(); err != nil {
		return err
	}

	// ExchangeChannel 会被 reconnect 置 nil，字段读取必须持锁快照后使用。
	r.Lock()
	pubCh := r.ExchangeChannel
	r.Unlock()

	ex, err := r.GetExchange(exchangeName)
	if err != nil {
		return err
	}

	if err := pubCh.DeclareExchange(ex.Name, ex.Type, ex.Durable, false); err != nil {
		return err
	}

	if err := pubCh.DeclareQueue(queueName, queueArgs, durableQueue, autoDel); err != nil {
		return err
	}

	if err := pubCh.BindQueue(queueName, routingKey, ex.Name, bindArgs); err != nil {
		return err
	}

	return nil
}

func (r *rabbitConnection) Publish(ctx context.Context, exchangeName, routingKey string, mandatory bool, msg amqp.Publishing) error {

	if err := r.lazyInitPublishChannel(); err != nil {
		return err
	}

	// ExchangeChannel 会被 reconnect 置 nil，字段读取必须持锁快照后使用。
	r.Lock()
	pubCh := r.ExchangeChannel
	r.Unlock()

	return pubCh.Publish(ctx, exchangeName, routingKey, mandatory, msg)
}

func (r *rabbitConnection) lazyInitPublishChannel() error {
	r.Lock()
	defer r.Unlock()
	if r.ExchangeChannel == nil {
		// 半连接态 fail-fast：Connect 拨号失败后 wrapper 已建但底层
		// amqp.Connection 仍为 nil。若不拦截，newRabbitChannel(nil) 会在
		// (*amqp.Connection).Channel() 处 nil 解引用 panic。
		if r.Connection == nil {
			return fmt.Errorf("%w: underlying AMQP connection is nil (Connect failed or never established)", ErrNotConnected)
		}
		var err error
		// lazy init publish channel
		r.ExchangeChannel, err = newRabbitChannel(r.Connection, r.qos)
		if err != nil {
			return err
		}

		// setup publisher confirms if enabled
		if r.confirmMode {
			if err := r.ExchangeChannel.Confirm(); err != nil {
				return err
			}
			confirmChan := make(chan amqp.Confirmation, 64)
			r.ExchangeChannel.NotifyPublish(confirmChan)
			go r.handleConfirms(confirmChan)
		}

		// setup return handler
		returnChan := make(chan amqp.Return, 64)
		r.ExchangeChannel.NotifyReturn(returnChan)
		go r.handleReturns(returnChan)
	}
	return nil
}

// handleConfirms processes publisher confirmations in a background goroutine.
func (r *rabbitConnection) handleConfirms(confirmChan chan amqp.Confirmation) {
	for conf := range confirmChan {
		if r.onConfirm != nil {
			r.onConfirm(conf)
		} else {
			if conf.Ack {
				logDebugf("message confirmed, delivery tag: %d", conf.DeliveryTag)
			} else {
				logWarnf("message nacked, delivery tag: %d", conf.DeliveryTag)
			}
		}
	}
}

// handleReturns processes returned messages in a background goroutine.
func (r *rabbitConnection) handleReturns(returnChan chan amqp.Return) {
	for ret := range returnChan {
		if r.onReturn != nil {
			r.onReturn(ret)
		} else {
			logErrorf("message returned, reason: %s, exchange: %s, routing key: %s",
				ret.ReplyText, ret.Exchange, ret.RoutingKey)
		}
	}
}
