// Package contract 提供 OpenTelemetry 埋点后端的契约装配：
// bootstrapv1.Instrumentation_Otlp 段 → metrics.Metrics 实例 + 段驱动
// Provider（AppProvider，见 provider.go）。
//
// 单独成包的原因（对齐 registry/etcd/contract 模式）：根包保持零契约依赖
// （纯 OTel SDK 实现），只有本包 import bconf + appkit。
//
// 与契约 metrics 段的分工：metrics 段是请求级 Recorder（中间件自动 emit，
// 维度固定）；本段是传输级埋点（名字与标签由埋点方自定）。
//
// 一条必须知道的库行为：根包的 New 会调用 otel.SetMeterProvider，把实例
// 的 MeterProvider 装进**进程全局**。同进程里若还有别的 OTel 指标使用方
// （例如 contrib/observability-otlp 的请求级通道），后构造者覆盖先构造者。
// 本包不改变该行为（根包仍是纯 SDK 实现）；但走契约装配时，pkg/appkit 在
// Provider 计划期把 `instrumentation.otlp` 与 `metrics` 两段按全局位单选
// 裁决——同时出现即启动失败，不留到运行期靠装配顺序赌运气。代码里直接
// 调两个构造函数仍然是调用方自己的纪律。
package contract

import (
	"fmt"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	otelmetrics "github.com/kalandramo/bald/metrics/otel"
)

// Type 是段路径 instrumentation.<本后端> 上的子段名（契约字段名 otlp）。
const Type = "otlp"

// Provider 按契约 instrumentation.otlp 子段构造 OTLP 埋点后端。
//
// appName/appVersion 是 app.name/app.version 的现值，用于 service_name /
// service_version 留空时的回落——回落发生在**这里**而不是后端默认值，因为
// 后端的默认（"bald-app"/"v0.0.1"）是库自己的哨兵，契约层把它当成「资源属性
// 是应用身份」来解读才是对的。
//
// endpoint 必填：留空时 OTLP exporter 会静默沿用库默认的 localhost:4317，
// 构造成功、埋点照常累加，导出失败只在 SDK 后台日志里出现——进程看起来
// 健康，指标一条都不落地。启动期挡住是这一格唯一可观察的时机。
func Provider(sec *bootstrapv1.Instrumentation_Otlp, appName, appVersion string) (*otelmetrics.Provider, func(), error) {
	if sec == nil {
		return nil, nil, fmt.Errorf("instrumentation.otlp: 子段为 nil（Provider 按段路径命中，请作为缺陷上报）")
	}
	endpoint := sec.GetEndpoint()
	if endpoint == "" {
		return nil, nil, fmt.Errorf("instrumentation.otlp: endpoint 必填（留空会静默导出到库默认的 localhost:4317，导出失败读不到同步错误）")
	}

	opts := []otelmetrics.Option{otelmetrics.WithEndpoint(endpoint)}
	if v := sec.GetServiceName(); v != "" {
		opts = append(opts, otelmetrics.WithServiceName(v))
	} else if appName != "" {
		opts = append(opts, otelmetrics.WithServiceName(appName))
	}
	if v := sec.GetServiceVersion(); v != "" {
		opts = append(opts, otelmetrics.WithServiceVersion(v))
	} else if appVersion != "" {
		opts = append(opts, otelmetrics.WithServiceVersion(appVersion))
	}
	if sec.GetInsecure() {
		opts = append(opts, otelmetrics.WithInsecure(true))
	}
	if sec.GetUseHttp() {
		opts = append(opts, otelmetrics.WithHTTP(true))
	}
	if interval := sec.GetExportIntervalSeconds(); interval != 0 {
		d := time.Duration(interval) * time.Second
		if d <= 0 {
			return nil, nil, fmt.Errorf("instrumentation.otlp: export_interval_seconds=%d 非法（≤0 的周期会让周期读取器不再导出）", interval)
		}
		opts = append(opts, otelmetrics.WithExportInterval(d))
	}

	p, err := otelmetrics.New(opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("instrumentation.otlp: %w", err)
	}
	return p, func() { _ = p.Close() }, nil
}
