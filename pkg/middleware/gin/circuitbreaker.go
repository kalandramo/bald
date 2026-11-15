package gin

import (
	"fmt"
	"math"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/kalandramo/bald/circuitbreaker"
	"github.com/kalandramo/bald/circuitbreaker/sentinel"

	sentinelapi "github.com/alibaba/sentinel-golang/api"
	scb "github.com/alibaba/sentinel-golang/core/circuitbreaker"
)

// CircuitBreakerConfig 配置服务端入口熔断层，对应契约
// server.http.middleware.circuit_breaker（gRPC 面同段去掉 status_code，
// 见 pkg/middleware/bundle 的 GRPCCircuitBreaker）。
//
// 0 的读法与契约一致：字段为 0 表示「未设置」，由本构造面取默认（阈值 0.5、
// 样本量 20、开窗 5s、滑窗 10s、状态码 503——见下方 default* 常量）。proto3
// 里没有字段存在性，「显式写 0」与「没写」不可区分，所以这里不能把 0 当配置
// 错误——那会让「只想改一个字段」的配置必须写满五个。负值与超界值才是错误。
//
// 底层算法是 `circuitbreaker/sentinel`（sentinel-golang 错误比例策略）：契约四
// 字段与策略参数一一映射——error_threshold→Threshold、min_requests→
// MinRequestAmount、window_ms→StatIntervalMs、open_duration_ms→
// RetryTimeoutMs；半开单探针语义相同（ProbeNum 留 0 即单探针）。sentinel 的
// 熔断器按**资源名**注册在进程级规则表里，本构造面为每个实例派生唯一资源名
// （bald.middleware.circuit_breaker.<n>）：HTTP/gRPC 两面各持独立实例、各判各
// 的失败率——与旧 hystrix 的「每实例独立滑窗」语义一致，不同配置的实例也不会
// 互相覆盖规则。
type CircuitBreakerConfig struct {
	// ErrorThreshold 是开窗的失败率阈值，取值 (0,1]。0 → 默认 0.5。
	// 契约字段 circuit_breaker.error_threshold。
	ErrorThreshold float64

	// MinRequests 是参与判定的最小样本量（滑窗内请求数低于它不开窗）。
	// 0 → 默认 20。契约字段 circuit_breaker.min_requests。
	MinRequests int

	// OpenDurationMs 是开窗时长（毫秒），到期后半开、只放一个探针。
	// 0 → 默认 5000。契约字段 circuit_breaker.open_duration_ms。
	OpenDurationMs int64

	// WindowMs 是失败率统计滑窗长度（毫秒）。0 → 默认 10000。
	// 契约字段 circuit_breaker.window_ms。
	WindowMs int64

	// StatusCode 是开态（含半开被拒）返回的 HTTP 状态码。0 → 默认 503；
	// 非 0 时必须是 5xx。契约字段 circuit_breaker.status_code。
	StatusCode int
}

// 开态默认值——契约注释声明的那一份。本构造面是它们的唯一来源（旧 hystrix
// 包自持默认值；后端收敛为 sentinel 后，四个契约字段没有「底层默认」可抄，
// 归一到这里的 default* 常量，避免注释与代码各自漂移）。
const (
	defaultErrorThreshold = 0.5
	defaultMinRequests    = 20
	defaultOpenDurationMs = 5000
	defaultWindowMs       = 10000
)

// DefaultCircuitBreakerStatusCode 是契约注释声明的开态默认状态码（503）。
const DefaultCircuitBreakerStatusCode = http.StatusServiceUnavailable

// Validate 检查显式给出的字段是否合法。0 一律合法（按未处理，见类型注释），
// 非法值返回错误 —— 调用方在启动期 fail-fast。
func (c CircuitBreakerConfig) Validate() error {
	if c.ErrorThreshold < 0 || c.ErrorThreshold > 1 {
		return fmt.Errorf("gin: circuit_breaker.error_threshold must be in (0,1], got %v", c.ErrorThreshold)
	}
	if c.MinRequests < 0 {
		return fmt.Errorf("gin: circuit_breaker.min_requests must be >= 0, got %d", c.MinRequests)
	}
	if c.OpenDurationMs < 0 {
		return fmt.Errorf("gin: circuit_breaker.open_duration_ms must be >= 0, got %d", c.OpenDurationMs)
	}
	if c.OpenDurationMs > math.MaxUint32 {
		return fmt.Errorf("gin: circuit_breaker.open_duration_ms must fit uint32, got %d", c.OpenDurationMs)
	}
	if c.WindowMs < 0 {
		return fmt.Errorf("gin: circuit_breaker.window_ms must be >= 0, got %d", c.WindowMs)
	}
	if c.WindowMs > math.MaxUint32 {
		return fmt.Errorf("gin: circuit_breaker.window_ms must fit uint32, got %d", c.WindowMs)
	}
	if c.StatusCode != 0 && (c.StatusCode < 500 || c.StatusCode > 599) {
		return fmt.Errorf("gin: circuit_breaker.status_code must be 5xx or 0, got %d", c.StatusCode)
	}
	return nil
}

// cbResourceSeq 是熔断实例资源名的序号源。sentinel 规则表按资源名全局共享，
// 序号派生保证每个构造出的实例独占一个资源（同配置实例也独立——两面的失败
// 率不互相喂养；跨实例共享熔断态属业务级决策，经 bundle.CircuitBreaker 注入
// 同一实例即可）。
var cbResourceSeq atomic.Uint32

// sentinelInitOnce 保证 sentinelapi.InitDefault 只跑一次：initSentinel 没有
// 幂等保护，重复调用会重启 metric 清算等核心后台组件。失败粘滞——环境性
// 失败（日志目录不可写等）重试也不会好，第二次构造直接回同一个错误。
var (
	sentinelInitOnce sync.Once
	sentinelInitErr  error
)

// initSentinelRuntime 幂等地初始化 sentinel 运行时（全局配置、日志、后台
// metric 组件）。只在真的构造熔断器时才调用：没配熔断段的进程不付这份
// 后台组件的成本。
func initSentinelRuntime() error {
	sentinelInitOnce.Do(func() { sentinelInitErr = sentinelapi.InitDefault() })
	return sentinelInitErr
}

// CircuitBreaker 持有中间件与其底层熔断器。
//
// 生命周期纪律：sentinel 的熔断器与规则表是进程级状态、无实例级资源需要
// 释放——Close 是幂等 no-op（不像旧 hystrix 那样把实例打成永久拒绝）。规则
// 在构造期经 LoadRules 注册进全局规则表，进程存活期一直生效；调用方停机时
// 调 Close 只是把「熔断器不再使用」这件事走一遍形式。
// CircuitBreaker 是服务端入口熔断的**构造面**：持有底层 sentinel 熔断器实例
// 与配置归一后的开态状态码。装配面在统一件（middleware.CircuitBreaker 经
// bundle 的面段/统一段注入）——本类型不再自带 gin 中间件（J2 重复税裁撤，
// 方案 S 收官后统一熔断件是唯一实现）。
type CircuitBreaker struct {
	breaker circuitbreaker.CircuitBreaker

	// statusCode 是开态（含半开被拒）的 HTTP 状态码（cfg.StatusCode 经默认值
	// 归一后的结果）。面段熔断换统一件后，本字段经 StatusCode() 逆映射成
	// 统一件的 openCode 语义（HTTPToCode），契约 status_code 字段语义在此保真。
	statusCode int

	closeOnce sync.Once
	closeErr  error
}

// StatusCode 返回开态 HTTP 状态码（默认 503；供统一件 openCode 逆映射）。
func (c *CircuitBreaker) StatusCode() int { return c.statusCode }

// NewCircuitBreaker 按配置构建熔断中间件（含底层 sentinel 熔断器与规则）。
func NewCircuitBreaker(cfg CircuitBreakerConfig) (*CircuitBreaker, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := initSentinelRuntime(); err != nil {
		return nil, fmt.Errorf("gin: init sentinel runtime: %w", err)
	}

	// 0 值取本文件的 default* 常量——默认值只有一个来源，不会与文档各自漂移。
	threshold := cfg.ErrorThreshold
	if threshold == 0 {
		threshold = defaultErrorThreshold
	}
	minRequests := cfg.MinRequests
	if minRequests == 0 {
		minRequests = defaultMinRequests
	}
	openMs := cfg.OpenDurationMs
	if openMs == 0 {
		openMs = defaultOpenDurationMs
	}
	windowMs := cfg.WindowMs
	if windowMs == 0 {
		windowMs = defaultWindowMs
	}

	resource := fmt.Sprintf("bald.middleware.circuit_breaker.%d", cbResourceSeq.Add(1))
	// LoadRulesOfResource 只替换**本资源**的规则——LoadRules 是整表替换，两个
	// 实例先后构造时后者会把前者的规则清掉（HTTP+gRPC 双段同装即两面静默失效）。
	if _, err := scb.LoadRulesOfResource(resource, []*scb.Rule{{
		Resource:         resource,
		Strategy:         scb.ErrorRatio,
		Threshold:        threshold,
		MinRequestAmount: uint64(minRequests),
		StatIntervalMs:   uint32(windowMs),
		RetryTimeoutMs:   uint32(openMs),
		// 10 桶对齐旧 hystrix 的滑窗精度；window_ms 不是 10 的整数倍时
		// sentinel 自己回落为单桶（只损精度、不损语义），这里不做二次校验。
		StatSlidingWindowBucketCount: 10,
	}}); err != nil {
		return nil, fmt.Errorf("gin: load circuit breaker rule for %s: %w", resource, err)
	}

	status := cfg.StatusCode
	if status == 0 {
		status = DefaultCircuitBreakerStatusCode
	}
	return &CircuitBreaker{
		breaker:    sentinel.New(resource),
		statusCode: status,
	}, nil
}

// Breaker 暴露底层熔断器（供 gRPC 面复用、观测与测试）。
func (c *CircuitBreaker) Breaker() circuitbreaker.CircuitBreaker { return c.breaker }

// Close 关闭底层熔断器（sentinel 下是幂等 no-op，见类型注释的生命周期纪律）。
// 多次调用只走一次，返回同一结果。
func (c *CircuitBreaker) Close() error {
	c.closeOnce.Do(func() {
		if c.breaker != nil {
			c.closeErr = c.breaker.Close()
		}
	})
	return c.closeErr
}
