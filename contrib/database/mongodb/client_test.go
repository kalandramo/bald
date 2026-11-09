// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package mongodb_test

import (
	"testing"

	"github.com/kalandramo/bald/contrib/database/mongodb"
)

// TestNewLazyConnect 验证 New 透传 bald-crud/mongodb 的惰性建连语义：driver v2
// Connect 不阻塞，无实连服务器也能构造客户端（构造期不校验可达性）。
func TestNewLazyConnect(t *testing.T) {
	c, err := mongodb.New()
	if err != nil {
		t.Fatalf("New 期望成功（惰性建连），实际错误: %v", err)
	}
	if c == nil {
		t.Fatal("New 返回 nil client")
	}
}
