package gin

import (
	"context"
	"strings"
	"testing"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/circuitbreaker"
	"github.com/kalandramo/bald/pkg/middleware"
)

// ---------------------------------------------------------------------------
// 服务端入口熔断（server.http.middleware.circuit_breaker 的中间件面）
//
// 两类判据分开写：
//   - 用**记录型假熔断器**钉「上报配对」这条契约义务（哪个响应码该记什么、
//     被本层拒掉的不能上报）——这些断言必须与底层算法无关，且能精确指出漏上报；
//   - 用**真 hystrix** 钉可观察形状（打满失败率后拒绝、4xx 不开窗、超时之外、
//     panic 之后探针不锁死）——只有真实现才会暴露半开锁死这类失效点。
//
// 时间参数：窗口一律取 10s（测试期间不旋转桶），sleepWindow 只在需要过半开时
// 用 50ms 并**轮询**而不是定长 sleep（CI 与 -race 下的调度抖动不该判红）。
// ---------------------------------------------------------------------------

// --- 配置校验（启动期 fail-fast）---

func TestCircuitBreakerConfig_Validate(t *testing.T) {
	okCases := []struct {
		name string
		cfg  CircuitBreakerConfig
	}{
		{"zero means defaults everywhere", CircuitBreakerConfig{}},
		{"threshold boundary 1", CircuitBreakerConfig{ErrorThreshold: 1}},
		{"500 status", CircuitBreakerConfig{StatusCode: 500}},
		{"599 status", CircuitBreakerConfig{StatusCode: 599}},
	}
	for _, tc := range okCases {
		if err := tc.cfg.Validate(); err != nil {
			t.Errorf("%s: 应合法，got %v", tc.name, err)
		}
	}

	badCases := []struct {
		name string
		cfg  CircuitBreakerConfig
		want string
	}{
		{"threshold over 1", CircuitBreakerConfig{ErrorThreshold: 1.5}, "error_threshold"},
		{"negative threshold", CircuitBreakerConfig{ErrorThreshold: -0.1}, "error_threshold"},
		{"negative min_requests", CircuitBreakerConfig{MinRequests: -1}, "min_requests"},
		{"negative open_duration_ms", CircuitBreakerConfig{OpenDurationMs: -1}, "open_duration_ms"},
		{"negative window_ms", CircuitBreakerConfig{WindowMs: -1}, "window_ms"},
		{"4xx status_code", CircuitBreakerConfig{StatusCode: 404}, "status_code"},
		{"sub-500 status_code", CircuitBreakerConfig{StatusCode: 42}, "status_code"},
	}
	for _, tc := range badCases {
		err := tc.cfg.Validate()
		if err == nil {
			t.Fatalf("%s: 应报错，got nil", tc.name)
		}
		if want := "circuit_breaker." + tc.want; !strings.Contains(err.Error(), want) {
			t.Errorf("%s: 错误文案应含 %q，got %q", tc.name, want, err.Error())
		}
	}
}

// TestNewCircuitBreaker_InvalidConfigFailsFast 显式配了非法值 → 构造即报错，
// 而不是「配了但不生效」。
func TestNewCircuitBreaker_InvalidConfigFailsFast(t *testing.T) {
	if _, err := NewCircuitBreaker(CircuitBreakerConfig{ErrorThreshold: 2}); err == nil {
		t.Fatal("error_threshold=2 应报错")
	}
	c, err := NewCircuitBreaker(CircuitBreakerConfig{})
	if err != nil {
		t.Fatalf("零值应取默认并构造成功: %v", err)
	}
	defer func() { _ = c.Close() }()
	if c.Breaker().State() != circuitbreaker.StateClosed {
		t.Errorf("新熔断器应 Closed，got %v", c.Breaker().State())
	}
}

// --- 上报配对义务（假熔断器，与算法无关）---





// --- 真 hystrix 的可观察形状 ---

// tripCfg 是测试用的极值：样本量 2、阈值默认 0.5、滑窗 10s（桶不旋转）、
// 开窗 60s（测试期间恒开）。
func tripCfg() CircuitBreakerConfig {
	return CircuitBreakerConfig{MinRequests: 2, OpenDurationMs: 60_000, WindowMs: 10_000}
}





// TestCircuitBreaker_CloseRejectsEverything hystrix 的 Close 之后永久拒绝。
// 记进测试是为了让「closer 只能在停止接流量之后调」这条纪律有可观察后果。
// 拒绝行为经统一件观察（本类型不再自带 gin 中间件——J2 裁撤后统一件是唯一实现）。
func TestCircuitBreaker_CloseRejectsEverything(t *testing.T) {
	c, err := NewCircuitBreaker(tripCfg())
	if err != nil {
		t.Fatal(err)
	}
	mw := middleware.CircuitBreaker(c.Breaker())
	next := func(context.Context, interface{}) (interface{}, error) { return nil, nil }

	if _, err := mw(next)(context.Background(), nil); err != nil {
		t.Fatalf("关闭前应放行，got %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close 应幂等，第二次 got %v", err)
	}
	_, err = mw(next)(context.Background(), nil)
	if err == nil {
		t.Fatal("Close 后应拒绝")
	}
	if e, ok := berrors.FromError(err); !ok || e.Code != berrors.CodeUnavailable {
		t.Errorf("Close 后拒绝码 = %v, want UNAVAILABLE", err)
	}
}
