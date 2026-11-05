// Package contract 提供 Datadog DogStatsD 埋点后端的契约装配：
// bootstrapv1.Instrumentation_Datadog 段 → metrics.Metrics 实例 + 段驱动
// Provider（AppProvider，见 provider.go）。
//
// 单独成包的原因（对齐 registry/etcd/contract 模式）：根包保持零契约依赖
// （纯 statsd client 实现），只有本包 import bconf + appkit。
//
// 与契约 metrics 段的分工：metrics 段是请求级 Recorder（中间件自动 emit，
// 维度固定）；本段是传输级埋点（名字与标签由埋点方自定）。
package contract

import (
	"fmt"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/metrics/datadog"
)

// Type 是段路径 instrumentation.<本后端> 上的子段名。
const Type = "datadog"

// Provider 按契约 instrumentation.datadog 子段构造 DogStatsD 埋点后端。
//
// address 必填：DogStatsD 走 UDP，agent 不在时**没有任何同步错误可读**——
// 每次埋点都「成功返回」，数据丢在没人监听的 127.0.0.1:8125 上。这一格与
// instrumentation.otlp 的 endpoint 同构：库默认值让「漏配」读起来像「配好了」。
func Provider(sec *bootstrapv1.Instrumentation_Datadog) (*datadog.Provider, func(), error) {
	if sec == nil {
		return nil, nil, fmt.Errorf("instrumentation.datadog: 子段为 nil（Provider 按段路径命中，请作为缺陷上报）")
	}
	addr := sec.GetAddress()
	if addr == "" {
		return nil, nil, fmt.Errorf("instrumentation.datadog: address 必填（留空会静默 UDP 到库默认的 127.0.0.1:8125，agent 不在时丢包无同步错误）")
	}

	opts := []datadog.Option{datadog.WithAddress(addr)}
	if ns := sec.GetNamespace(); ns != "" {
		opts = append(opts, datadog.WithNamespace(ns))
	}
	if size := sec.GetBufferSize(); size != 0 {
		if size < 0 {
			return nil, nil, fmt.Errorf("instrumentation.datadog: buffer_size=%d 非法（负数会被后端的 >0 判据当成「不缓冲」，与 0 同义——要么给正数要么别写）", size)
		}
		opts = append(opts, datadog.WithBufferSize(int(size)))
	}
	if millis := sec.GetFlushPeriodMillis(); millis != 0 {
		d := time.Duration(millis) * time.Millisecond
		if d <= 0 {
			return nil, nil, fmt.Errorf("instrumentation.datadog: flush_period_millis=%d 非法（非正周期的聚合线程会在 time.NewTicker 上 panic）", millis)
		}
		opts = append(opts, datadog.WithFlushPeriod(d))
	}
	if rate := sec.GetSampleRate(); rate != 0 {
		if rate < 0 || rate > 1 {
			return nil, nil, fmt.Errorf("instrumentation.datadog: sample_rate=%v 非法（statsd 的采样率须在 (0,1]，越界时每次写入都返回错误且被后端丢弃）", rate)
		}
		opts = append(opts, datadog.WithSampleRate(rate))
	}

	p, err := datadog.New(opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("instrumentation.datadog: %w", err)
	}
	return p, func() { _ = p.Close() }, nil
}
