package appkit

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	bconf "github.com/kalandramo/bald/bconf"
	log "github.com/kalandramo/bald/log"
)

// 运行期装配（W1）：server 构造必须发生在 beforeStart 钩子链**之后**。
//
// 为什么：业务声明的路由/gRPC service 依赖 beforeStart 建立的运行期资源
// （DB/Redis/仓储）。若 server 构造仍在 FromBootstrap 构造期执行，消费方
// （register 回调、handler）就早于依赖就绪——业务被迫改用请求期懒解析
// （lazyAuthn/lazySigner 适配器族 + 请求期读包级 store）绕开该时序。
//
// 断言（RED 时期望失败）：
//   - 构造期：register 未被调用；
//   - Run 期：顺序恰为 beforeStart → register。
func TestFromBootstrap_ServerConstructionAfterBeforeStart(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)

	var mu sync.Mutex
	var order []string
	mark := func(s string) {
		mu.Lock()
		order = append(order, s)
		mu.Unlock()
	}
	snapshot := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(order, ",")
	}

	a, err := FromBootstrap(cfg,
		WithConfigNamespace(testConfigNamespace), WithHTTP(new(http.ServeMux)),
		WithGRPC(func(*grpc.Server) { mark("register") }),
		WithBeforeStart(func(context.Context) error { mark("beforeStart"); return nil }),
	)
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}

	if got := snapshot(); strings.Contains(got, "register") {
		t.Fatalf("server construction ran during FromBootstrap (order=%q); want deferred to Run", got)
	}

	runBriefly(t, a, 50*time.Millisecond)

	if got := snapshot(); got != "beforeStart,register" {
		t.Fatalf("wiring order = %q, want \"beforeStart,register\"", got)
	}
}
