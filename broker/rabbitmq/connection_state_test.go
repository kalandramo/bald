package rabbitmq

import (
	"crypto/tls"
	"sync"
	"testing"
	"time"

	"github.com/kalandramo/bald/broker"
)

// ---- isConnected：C-5 修复点，connected 必须持锁读 ----

func TestIsConnectedLockedRead(t *testing.T) {
	r := newRabbitMQConnection(broker.NewOptionsAndApply())
	if r.isConnected() {
		t.Fatal("初始 connected 应为 false")
	}
	r.Lock()
	r.connected = true
	r.Unlock()
	if !r.isConnected() {
		t.Fatal("持锁置位后 isConnected 应为 true")
	}
}

// TestConcurrentStateAccessors 钉住 C-5：waitChan/isConnected 的字段会被
// connect/reconnect/Close 持锁置换，读侧必须走快照；并发混读在 -race 下不得报
// 数据竞争。signalConnectedLocked 只在持锁时调用，同样纳入并发压测。
func TestConcurrentStateAccessors(t *testing.T) {
	r := newRabbitMQConnection(broker.NewOptionsAndApply())
	r.Lock()
	r.connected = true
	r.signalConnectedLocked()
	r.Unlock()

	const n = 8
	const rounds = 50
	var wg sync.WaitGroup
	for g := 0; g < n; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				_ = r.isConnected()
				<-r.waitChan() // 锁存态下立即可读
				r.Lock()
				r.signalConnectedLocked() // 判重，不得 close of closed panic
				r.Unlock()
			}
		}()
	}
	wg.Wait()
}

// ---- tryConnect 失败分支：不伪造握手，靠拒连端口走 cfgCopy / TLS 分支 ----

func TestTryConnectFailureBranches(t *testing.T) {
	addr := refusedURL(t)

	t.Run("nil_config_copies_default", func(t *testing.T) {
		r := newRabbitMQConnection(broker.NewOptionsAndApply(broker.WithAddress(addr)))
		if err := r.tryConnect(false, nil); err == nil {
			t.Fatal("拒连地址 tryConnect(nil config) 应失败")
		}
		// 拷贝语义：TLS 写入不得污染包级默认值。
		if DefaultAmqpConfig.TLSClientConfig != nil {
			t.Fatal("config==nil 分支不得把 InsecureSkipVerify 写进 DefaultAmqpConfig")
		}
	})

	t.Run("secure_sets_tls", func(t *testing.T) {
		r := newRabbitMQConnection(broker.NewOptionsAndApply(broker.WithAddress(addr)))
		cfg := DefaultAmqpConfig
		if err := r.tryConnect(true, &cfg); err == nil {
			t.Fatal("拒连地址 secure tryConnect 应失败")
		}
		// AR-9：nil TLSClientConfig 必须走标准证书校验（SNI 取 URL host），
		// 不再默认 InsecureSkipVerify——跳过校验应由契约显式声明。
		if cfg.TLSClientConfig == nil {
			t.Fatal("secure 分支应写入 TLS 配置")
		}
		if cfg.TLSClientConfig.InsecureSkipVerify {
			t.Fatal("nil TLSClientConfig 不得默认跳过证书校验（AR-9）")
		}
		if cfg.TLSClientConfig.ServerName != "127.0.0.1" {
			t.Fatalf("SNI 应取 URL host，got %q", cfg.TLSClientConfig.ServerName)
		}
	})

	t.Run("amqps_url_defaults_to_verified_tls", func(t *testing.T) {
		// amqps:// 是 AR-9 的主触发面：用户以为开了 TLS，实际此前对端未被验证。
		r := newRabbitMQConnection(broker.NewOptionsAndApply(broker.WithAddress("amqps://user:pass@rabbit.internal:5671/vhost")))
		cfg := DefaultAmqpConfig
		if err := r.tryConnect(false, &cfg); err == nil {
			t.Fatal("拒连地址 amqps tryConnect 应失败")
		}
		if cfg.TLSClientConfig == nil {
			t.Fatal("amqps:// 应自动启用 TLS")
		}
		if cfg.TLSClientConfig.InsecureSkipVerify {
			t.Fatal("amqps:// 不得默认跳过证书校验（AR-9）")
		}
		if cfg.TLSClientConfig.ServerName != "rabbit.internal" {
			t.Fatalf("SNI 应取 URL host（不含端口/凭证/vhost），got %q", cfg.TLSClientConfig.ServerName)
		}
	})

	t.Run("tls_config_reuses_existing", func(t *testing.T) {
		r := newRabbitMQConnection(broker.NewOptionsAndApply(broker.WithAddress(addr)))
		cfg := DefaultAmqpConfig
		cfg.TLSClientConfig = &tls.Config{ServerName: "keep-me"}
		if err := r.tryConnect(false, &cfg); err == nil {
			t.Fatal("拒连地址 tryConnect 应失败")
		}
		if cfg.TLSClientConfig.ServerName != "keep-me" {
			t.Fatal("已有 TLSClientConfig 不得被覆盖")
		}
	})
}

// ---- connect 失败后状态不落真 ----

func TestConnectFailureLeavesStateClean(t *testing.T) {
	r := newRabbitMQConnection(broker.NewOptionsAndApply(broker.WithAddress(refusedURL(t))))
	if err := r.Connect(false, nil); err == nil {
		t.Fatal("Connect 指向拒连地址应失败")
	}
	if r.isConnected() {
		t.Fatal("连接失败后 connected 不得置真")
	}
	if r.Connection != nil {
		t.Fatal("连接失败后不应残留 amqp.Connection")
	}
}

// ---- Connect 幂等：connected 为真时快速返回，不再拨号 ----

func TestConnectIdempotentWhenConnected(t *testing.T) {
	r := newRabbitMQConnection(broker.NewOptionsAndApply())
	r.Lock()
	r.connected = true
	r.Unlock()
	// connected 为真直接返回 nil；地址是默认值，若真拨号会拖慢/抖动。
	if err := r.Connect(false, nil); err != nil {
		t.Fatalf("已连接时 Connect 应幂等 nil，got %v", err)
	}
}

// ---- Close 的幂等与空连接分支 ----

func TestCloseIdempotentAndNilConnection(t *testing.T) {
	r := newRabbitMQConnection(broker.NewOptionsAndApply())
	r.Lock()
	r.connected = true
	r.Unlock()

	if err := r.Close(); err == nil {
		t.Fatal("Connection 为 nil 时 Close 应报错")
	}
	if r.isConnected() {
		t.Fatal("Close 后 connected 应复位")
	}
	// 第二次：r.close 已关闭，select 命中后幂等返回，不再触碰 nil Connection。
	if err := r.Close(); err != nil {
		t.Fatalf("重复 Close 应幂等 nil，got %v", err)
	}
}

// ---- reconnect：首轮 close 已关闭时直接退出（离线可达的退出路径）----

func TestReconnectExitsOnClose(t *testing.T) {
	r := newRabbitMQConnection(broker.NewOptionsAndApply())
	// 首轮 connect=false，不会 tryConnect；close 关闭后 select 立即命中退出。
	r.Lock()
	close(r.close)
	r.Unlock()
	done := make(chan struct{})
	go func() {
		r.reconnect(false, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reconnect 在 close 后未及时退出")
	}
}
