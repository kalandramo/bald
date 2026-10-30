package user

import (
	"github.com/spf13/cobra"
)

// NewUserCommand 返回 `user` 子命令：一组固定 CRUD/过滤/排序/分页演示。
// 后端由 buildProvider 提供（provider_mem.go 默认；-tags gorm 走 GORM+SQLite）。
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
