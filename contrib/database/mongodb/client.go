// Package mongodb 提供 bald-crud/mongodb 客户端的 bald 侧直连封装：
// 类型别名与构造透传，零契约依赖（契约映射见 contract 子包）。
package mongodb

import (
	mongocrud "github.com/kalandramo/bald-crud/mongodb"
)

// Client 是 MongoDB 客户端（bald-crud/mongodb Client 别名）。
type Client = mongocrud.Client

// Option 透传 bald-crud/mongodb 的函数式选项。
type Option = mongocrud.Option

// New 按 options 构造 MongoDB 客户端（driver v2 Connect 语义：后台惰性
// 建连，构造不阻塞；可用性检查用 Client.CheckConnect）。
func New(opts ...Option) (*Client, error) {
	return mongocrud.NewClient(opts...)
}
