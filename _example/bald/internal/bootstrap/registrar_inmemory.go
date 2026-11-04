//go:build !nacos

package bootstrap

import (
	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/registry/inmemory"
)

// registrarOptions 是默认构建的注册中心接线：进程内实例，克隆即可跑，
// 不需要任何外部后端。
//
// 这条钉住只在**没有** nacos tag 时成立：显式 Registrar 实例会把契约 registry 段
// 标成「已消费」，于是同段驱动的 nacos Provider 在计划期被跳过。两边同时接是自
// 我矛盾的声明——契约选了 nacos，实际注册的却是内存实例（见 register_nacos.go）。
func registrarOptions() []appkit.Option {
	return []appkit.Option{appkit.Registrar(inmemory.New())}
}
