// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package gorm_test

import (
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	gormcrud "github.com/kalandramo/bald-crud/gorm"

	contribgorm "github.com/kalandramo/bald/contrib/database/gorm"
)

// TestNewAndCloseSuccess 覆盖 New 的成功构造 + Close 的正常关闭：经 bald-crud/gorm
// 的 "go_sqlite" 方言开一个落盘库（纯 Go 驱动，无 CGO、不触网），Close 走
// sqlDB.Close() 成功分支。
func TestNewAndCloseSuccess(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "test.db")
	c, err := contribgorm.New(
		gormcrud.WithDriverName("go_sqlite"),
		gormcrud.WithDSN(dsn),
	)
	if err != nil {
		t.Fatalf("New 期望成功，实际错误: %v", err)
	}
	if c == nil {
		t.Fatal("New 返回 nil client")
	}
	if err := contribgorm.Close(c); err != nil {
		t.Fatalf("Close 期望成功，实际错误: %v", err)
	}
}

// TestCloseNilClient 覆盖 Close 的 c==nil 兜底：返回 nil 不 panic。
func TestCloseNilClient(t *testing.T) {
	if err := contribgorm.Close(nil); err != nil {
		t.Fatalf("Close(nil) 期望 nil，实际 %v", err)
	}
}

// TestCloseNilDB 覆盖 Close 的 c.DB==nil 兜底：零值 Client 内嵌 *gorm.DB 为空。
func TestCloseNilDB(t *testing.T) {
	if err := contribgorm.Close(&contribgorm.Client{}); err != nil {
		t.Fatalf("Close(零值 client) 期望 nil，实际 %v", err)
	}
}

// TestCloseDBError 覆盖 Close 的 c.DB.DB() 出错分支：直接构造一个 ConnPool 为 nil
// 的合法 *gorm.DB（非空 *Config，ConnPool 非 *sql.DB），使 DB() 返回 ErrInvalidDB，
// Close 透传该错误。
func TestCloseDBError(t *testing.T) {
	c := &contribgorm.Client{DB: &gorm.DB{Config: &gorm.Config{}}}
	if err := contribgorm.Close(c); err == nil {
		t.Fatal("Close 期望透传 DB() 错误，实际返回 nil")
	}
}
