// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// unified.go 把契约 server.unified_middleware 段翻译成统一链（方案 S 批次 3
// 的装配面）：规则按 operation 选择器进 matcher，Gin()/GRPCInterceptors()/
// GRPCStreamInterceptors() 把这条链经两座桥预置到**双面链头**——先于两面
// middleware 段的中间件；统一链中间件短路时面段不执行。
//
// 段缺省（opt-in）时不挂统一链，现有部署零影响。规则错误（未知策略、
// validate 未注入回调）在链构建时 fail-fast——声明了却静默不生效是最坏
// 结果（判据 J7 同形）。

package bundle

import (
	"errors"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/pkg/middleware"
	mwmatcher "github.com/kalandramo/bald/pkg/middleware/matcher"
	"github.com/kalandramo/bald/ratelimit/tokenbucket"
)

// errUnifiedValidatorMissing 是「unified_middleware 段声明了 validate 策略、
// 却未注入校验回调」的装配错误——声明了却静默不生效是最坏结果（判据 J7 同形）。
var errUnifiedValidatorMissing = errors.New(
	"bundle: UNIFIED_VALIDATOR_MISSING —— unified_middleware 声明了 validate 策略，" +
		"但未注入校验回调：用 WithUnifiedValidator(func(ctx, req) error) 注入")

// WithUnifiedMiddleware 注入契约统一段（server.unified_middleware），三座链头
// （Gin/GRPCInterceptors/GRPCStreamInterceptors）预置统一链。cfg 为 nil（段
// 缺省）时不挂——现有部署零影响。
func WithUnifiedMiddleware(cfg *bootstrapv1.Server_UnifiedMiddleware) Option {
	return func(b *Bundle) { b.unifiedCfg = cfg }
}

// WithUnifiedValidator 注入统一段 validate 策略的校验回调。段内未声明
// validate 时无需注入。
func WithUnifiedValidator(v middleware.MessageValidator) Option {
	return func(b *Bundle) { b.unifiedValidator = v }
}

// unifiedMatcher 把统一段编译成 matcher（启动期一次；规则错误 fail-fast）。
// b.unifiedCfg 为 nil 时返回 (nil, nil)。
func (b *Bundle) unifiedMatcher() (*mwmatcher.Matcher, error) {
	if b.unifiedCfg == nil {
		return nil, nil
	}
	m := mwmatcher.New()
	for i, rule := range b.unifiedCfg.GetRules() {
		sel := rule.GetSelector()
		var mw middleware.Middleware
		switch policy := rule.GetPolicy().(type) {
		case *bootstrapv1.Server_UnifiedMiddleware_Rule_Recovery:
			mw = middleware.Recovery()
		case *bootstrapv1.Server_UnifiedMiddleware_Rule_RequestId:
			mw = middleware.RequestID(middleware.RequestIDConfig{HeaderName: policy.RequestId.GetHeaderName()})
		case *bootstrapv1.Server_UnifiedMiddleware_Rule_RateLimit:
			limiter, err := tokenbucket.New(policy.RateLimit.GetRate(), float64(policy.RateLimit.GetBurst()))
			if err != nil {
				return nil, fmt.Errorf("unified_middleware rule[%d] (%q): %w", i, sel, err)
			}
			mw = middleware.RateLimit(limiter, policy.RateLimit.GetWait())
		case *bootstrapv1.Server_UnifiedMiddleware_Rule_Timeout:
			mw = middleware.Timeout(time.Duration(policy.Timeout.GetDefaultTimeoutMs()) * time.Millisecond)
		case *bootstrapv1.Server_UnifiedMiddleware_Rule_Validate:
			if b.unifiedValidator == nil {
				return nil, fmt.Errorf("unified_middleware rule[%d] (%q): %w", i, sel, errUnifiedValidatorMissing)
			}
			mw = middleware.Validator(b.unifiedValidator)
		default:
			return nil, fmt.Errorf("unified_middleware rule[%d] (%q): unknown policy（契约演进先于代码？）", i, sel)
		}
		m.Add(sel, mw)
	}
	return m, nil
}

// prependUnifiedGin 把统一链经 GinHandler 桥预置到 gin 链头（非 nil matcher 时）。
func prependUnifiedGin(chain []gin.HandlerFunc, m *mwmatcher.Matcher) ([]gin.HandlerFunc, error) {
	if m == nil {
		return chain, nil
	}
	return append(chain, middleware.GinHandler(mwmatcher.Match(m))), nil
}

// prependUnifiedUnary 把统一链经 UnaryServerInterceptor 桥预置到 gRPC unary 链头。
func prependUnifiedUnary(chain []grpc.UnaryServerInterceptor, m *mwmatcher.Matcher) ([]grpc.UnaryServerInterceptor, error) {
	if m == nil {
		return chain, nil
	}
	return append(chain, middleware.UnaryServerInterceptor(mwmatcher.Match(m))), nil
}

// prependUnifiedStream 把统一链经 StreamServerInterceptor 桥预置到 gRPC 流链头。
func prependUnifiedStream(chain []grpc.StreamServerInterceptor, m *mwmatcher.Matcher) ([]grpc.StreamServerInterceptor, error) {
	if m == nil {
		return chain, nil
	}
	return append(chain, middleware.StreamServerInterceptor(mwmatcher.Match(m))), nil
}
