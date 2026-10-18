// Package contract 提供 observability-otlp 后端的契约装配：bootstrapv1 的
// tracer / metrics 段 → appkit TracerRegistry / MetricsRegistry Provider。
//
// 单独成包的原因（对齐 registry/nacos/contract 模式）：trace/metrics
// 根包保持零契约依赖（纯 SDK 实现），只有本包 import bconf + appkit——业务
// 按需 import，依赖图全程可见。
//
// serviceName（resource 的 service.name）来自应用而非契约段，故导出的是
// Provider 构造器（闭包绑定服务名）而非包级 Provider 变量：
//
//	tr := appkit.NewTracerRegistry()
//	tr.MustRegister(otlpcontract.TracerType, otlpcontract.NewTracerProvider("my-app"))
//	mr := appkit.NewMetricsRegistry()
//	mr.MustRegister(otlpcontract.TypePrometheus, otlpcontract.NewPrometheusProvider("my-app"))
//	mr.MustRegister(otlpcontract.TypeOTLP, otlpcontract.NewOTLPProvider("my-app"))
package contract

import (
	"context"
	"fmt"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/pkg/appkit"

	obmetrics "github.com/kalandramo/bald/contrib/observability-otlp/metrics"
	obtrace "github.com/kalandramo/bald/contrib/observability-otlp/trace"

	"github.com/kalandramo/bald/observability/metricshttp"
)

// 契约段 type 字段的取值。
const (
	// TracerType 是 tracer.type 中 OTLP 直推后端的取值（当前唯一）。
	TracerType = "otlp"
	// TypePrometheus 是 metrics.type 中「仅本地抓取」的取值。
	TypePrometheus = "prometheus"
	// TypeOTLP 是 metrics.type 中「抓取 + OTLP 直推双通道」的取值。
	TypeOTLP = "otlp"
)

// NewTracerProvider 返回绑定 serviceName 的 trace 后端 Provider
// （tracer.type=otlp：endpoint 必填，缺省 fail-fast）。
func NewTracerProvider(serviceName string) appkit.TracerProvider {
	return func(_ context.Context, cfg *bootstrapv1.Tracer) (func(context.Context) error, error) {
		otlp := cfg.GetOtlp()
		endpoint := otlp.GetEndpoint()
		if endpoint == "" {
			return nil, fmt.Errorf(`tracer.type=%q requires tracer.otlp.endpoint`, TracerType)
		}
		return obtrace.Setup(
			obtrace.WithOTLPAddr(endpoint),
			obtrace.WithServiceName(serviceName),
			obtrace.WithInsecure(otlp.GetInsecure()),
			obtrace.WithHeaders(otlp.GetHeaders()),
			obtrace.WithSampler(otlp.GetSampler()),
			obtrace.WithSampleRatio(otlp.GetSampleRatio()),
		)
	}
}

// NewPrometheusProvider 返回绑定 serviceName 的「仅本地抓取」指标 Provider
// （metrics.type=prometheus：otlp.endpoint 必须为空，配置了直推端点属矛盾
// 声明，fail-fast 提示改用 type "otlp"）。
//
// B7：产物为可关闭的暴露端（appkit.MetricsExposure，实现是 metricshttp.Server）
// ——监听面由本包经 metricshttp.Start 在独立端口起停（addr/path 取契约
// metrics.prometheus 段，空值走缺省 :9091 / /metrics）；引擎保证停机序
// 「先停暴露端、再 flush」。
func NewPrometheusProvider(serviceName string) appkit.MetricsProvider {
	return func(_ context.Context, cfg *bootstrapv1.Metrics) (appkit.MetricsExposure, func(context.Context) error, error) {
		if ep := cfg.GetOtlp().GetEndpoint(); ep != "" {
			return nil, nil, fmt.Errorf(`metrics.type=%q conflicts with metrics.otlp.endpoint %q (use type %q to enable push)`,
				TypePrometheus, ep, TypeOTLP)
		}
		h, shutdown, err := obmetrics.Setup(obmetrics.WithServiceName(serviceName))
		if err != nil {
			return nil, nil, err
		}
		return metricshttp.Start(cfg.GetPrometheus().GetAddr(), cfg.GetPrometheus().GetPath(), h), shutdown, nil
	}
}

// NewOTLPProvider 返回绑定 serviceName 的「抓取 + 直推双通道」指标 Provider
// （metrics.type=otlp：endpoint 必填，缺省 fail-fast；prometheus 段的
// addr/path 由本 Provider 经 metricshttp.Start 消费起暴露端，见
// NewPrometheusProvider 的 B7 说明）。
func NewOTLPProvider(serviceName string) appkit.MetricsProvider {
	return func(_ context.Context, cfg *bootstrapv1.Metrics) (appkit.MetricsExposure, func(context.Context) error, error) {
		otlp := cfg.GetOtlp()
		endpoint := otlp.GetEndpoint()
		if endpoint == "" {
			return nil, nil, fmt.Errorf(`metrics.type=%q requires metrics.otlp.endpoint`, TypeOTLP)
		}
		interval := time.Duration(otlp.GetPushInterval()) * time.Second
		h, shutdown, err := obmetrics.Setup(
			obmetrics.WithOTLPAddr(endpoint),
			obmetrics.WithServiceName(serviceName),
			obmetrics.WithInsecure(otlp.GetInsecure()),
			obmetrics.WithHeaders(otlp.GetHeaders()),
			obmetrics.WithInterval(interval),
		)
		if err != nil {
			return nil, nil, err
		}
		return metricshttp.Start(cfg.GetPrometheus().GetAddr(), cfg.GetPrometheus().GetPath(), h), shutdown, nil
	}
}
