// Package gorm 提供 bald-crud/gorm（通用 SQL 客户端）的 bald 侧直连封装：
// 类型别名与构造透传，零契约依赖（契约映射见 contract 子包）。
package gorm

import (
	gormcrud "github.com/kalandramo/bald-crud/gorm"
)

// Client 是 SQL 数据库客户端（bald-crud/gorm Client 别名，内嵌 *gorm.DB）。
type Client = gormcrud.Client

// Option 透传 bald-crud/gorm 的函数式选项。
type Option = gormcrud.Option

// New 按 options 构造 SQL 客户端。迁移模型是代码声明（契约表达不了），
// 由业务以 gormcrud.WithAutoMigrate / mixin 追加；契约 migrate 开关经
// WithEnableMigrate 生效（见 contract.Provider）。
func New(opts ...Option) (*Client, error) {
	return gormcrud.NewClient(opts...)
}

// Close 释放客户端底层连接池（Client 内嵌 *gorm.DB，经 sql.DB 关闭）。
// cleanup 语义：调用后 client 不可再用。
func Close(c *Client) error {
	if c == nil || c.DB == nil {
		return nil
	}
	sqlDB, err := c.DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
