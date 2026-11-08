// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package retry

import (
	"context"
	"errors"
)

// ErrBulkheadFull 在并发预算耗尽时返回——哨兵集中定义，调用方用 errors.Is 匹配。
var ErrBulkheadFull = errors.New("retry: bulkhead full")

// Bulkhead 以舱壁/信号量模式隔离一个下游服务：并发在途请求封顶，预算耗尽时
// 快速失败而不是排队——慢或卡死的下游不会拖垮进程、不会把故障级联给无关
// 调用方。代码自 onexmesh resilience 移植；形态对齐本 module 的命令式 Do
// 风格（与 Hedger 并列）。
//
// 语义边界（与 ratelimit 的分工）：ratelimit 的 bbr/sentinel 是**入站**降载
// 与限流（server 侧），Bulkhead 是**出站**调用隔离（client 侧）——一个保护
// 自己不被打垮，一个保护自己不去拖垮别人。
type Bulkhead struct {
	sem chan struct{}
}

// NewBulkhead 构造舱壁。maxConcurrent <= 0 视为禁用（Do 直通，无隔离）。
func NewBulkhead(maxConcurrent int) *Bulkhead {
	if maxConcurrent < 1 {
		return &Bulkhead{}
	}
	return &Bulkhead{sem: make(chan struct{}, maxConcurrent)}
}

// Disabled 报告舱壁是否处于禁用态（maxConcurrent <= 0）。
func (b *Bulkhead) Disabled() bool { return b.sem == nil }

// Do 在预算内执行 fn：预算耗尽快速失败返回 ErrBulkheadFull，不排队。
func (b *Bulkhead) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	if b.sem == nil {
		return fn(ctx)
	}
	select {
	case b.sem <- struct{}{}:
		defer func() { <-b.sem }()
		return fn(ctx)
	default:
		return ErrBulkheadFull
	}
}
