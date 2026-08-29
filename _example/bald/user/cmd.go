package user

import (
	"github.com/spf13/cobra"
)

// NewUserCommand 返回 `bald user` 子命令。
// 展示 bald 存储层「后端可切换」：具体 Provider 由 buildProvider() 提供，
// 该函数有两个实现（build tag 隔离）：
//   - provider_mem.go   (//go:build !gorm)  —— 内存实现，默认构建启用
//   - provider_gorm.go  (//go:build gorm)   —— GORM+SQLite 实现，需 -tags gorm
//
// 业务代码（entity.go 的 runDemo）完全不感知后端差异。
func NewUserCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "user",
		Short: "演示 bald 存储层：内存/GORM 后端可切换",
		Long: "执行一组固定 CRUD/过滤/排序/分页演示，验证 pkg/store 抽象。\n" +
			"默认（无 tag）使用内存后端；-tags gorm 使用 GORM+SQLite 后端。",
		RunE: func(_ *cobra.Command, _ []string) error {
			println("[bald user] 使用后端:", backendName())
			p, err := buildProvider()
			if err != nil {
				return err
			}
			defer p.Close()
			return runDemo(p)
		},
	}
}
