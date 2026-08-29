//go:build !gorm

package user

import (
	"github.com/kalandramo/bald/pkg/store"
	"github.com/kalandramo/bald/pkg/store/inmemory"
)

// backendName 返回当前后端名（演示打印用）。
func backendName() string { return "inmemory" }

// buildProvider 构造内存版 Provider（默认构建启用）。
func buildProvider() (store.DBProvider[User], error) {
	return inmemory.NewProvider[User](keyOf), nil
}
