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

// WithGRPCOptions（W1）：gRPC 选项工厂必须在 **Run 期**求值，而非 FromBootstrap 构造期。
//
// 为什么：选项常含拦截器链，而链需要认证器/授权器——它们在业务 beforeStart 才建立。
// WithGRPC 的 unary 变参在调用点即求值（Go 语义），构造期只能拿到 nil，业务被迫用
// 「请求期解析」代理（lateAuthn/lateAuthz）。WithGRPCOptions 收工厂函数，延后到
// server 构造钩子内调用，使选项可见运行期资源。
//
// 断言（RED 时期望失败）：
//   - 构造期：选项工厂**未被调用**；
//   - Run 期：调用顺序为 beforeStart → optionsFn → register。
func TestFromBootstrap_GRPCOptionsEvaluatedAtRunTime(t *testing.T) {
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
		WithGRPCOptions(func() []grpc.ServerOption {
			mark("optionsFn")
			return nil // 只验时机，不注入真拦截器
		}),
		WithBeforeStart(func(context.Context) error { mark("beforeStart"); return nil }),
	)
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}

	// 构造期：选项工厂不得被调用（这正是本能力的意义）。
	if got := snapshot(); strings.Contains(got, "optionsFn") {
		t.Fatalf("gRPC options factory ran during FromBootstrap (order=%q); "+
			"want deferred to Run -- 否则构造期拿不到运行期依赖", got)
	}

	runBriefly(t, a, 50*time.Millisecond)

	// Run 期：beforeStart（建依赖）先于 optionsFn（用依赖），optionsFn 先于 register
	//（选项必须在 grpc.NewServer 之前求值，register 在其后）。
	if got := snapshot(); got != "beforeStart,optionsFn,register" {
		t.Fatalf("wiring order = %q, want \"beforeStart,optionsFn,register\"", got)
	}
}
