//go:build gorm

package user

import (
	"github.com/kalandramo/bald/pkg/store"
	baldgorm "github.com/kalandramo/bald-store-gorm"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// backendName 返回当前后端名（演示打印用）。
func backendName() string { return "gorm(sqlite)" }

// buildProvider 构造 GORM+SQLite 版 Provider（需 -tags gorm 构建）。
func buildProvider() (store.DBProvider[User], error) {
	// 用进程内内存库（cache=shared），保证每次运行从干净数据集开始，
	// 不残留上次运行的行导致 Create 主键冲突。单连接避免 memory 库跨连接不可见。
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_fk=1"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, err
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	p := baldgorm.NewGormProvider[User](db, keyOf)
	if err := p.Migrate(contextBackground()); err != nil {
		return nil, err
	}
	return p, nil
}
