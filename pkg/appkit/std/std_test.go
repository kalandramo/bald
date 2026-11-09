// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package std_test

import (
	"testing"

	"github.com/kalandramo/bald/pkg/appkit/std"
)

// TestStandardReturnsOption 验证 Standard() 返回非 nil Option（框架自有后端的默认
// Provider 打包面——HTTP/gRPC/Audit/Logger 四段的集合）。
func TestStandardReturnsOption(t *testing.T) {
	if std.Standard() == nil {
		t.Fatal("std.Standard() 期望非 nil Option")
	}
}
