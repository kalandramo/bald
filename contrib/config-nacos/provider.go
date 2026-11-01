// Package confignacos 提供 nacos 配置源的契约装配：bootstrapv1 的 config.nacos
// 段 → []config.Layer（经 appkit 段驱动 Provider 接入）。
//
//	appkit.MustServe(ctx, std.Standard(), confignacos.AppProvider())
//
// 层语义与 bootstrap.NacosProvider 一致：nacos 源参与热更新（Watch=true），
// format 透传层格式；未配置 config.nacos 时返回空层列表（config 段 Provider
// 无条件构建，源可独立于该段存在）。
//
// 单独成包/module 的原因：bootstrap 与 bconfig 都被 appkit 依赖，把 Provider
// （需要 appkit）放回任一者会形成模块环；本包只做契约映射，后端细节仍在
// bconfig/nacos。
package confignacos

import (
	"context"
	"fmt"

	"github.com/kalandramo/bald/bootstrap"
	baldconfig "github.com/kalandramo/bald/bootstrap/config"
	"github.com/kalandramo/bald/pkg/appkit"
)

// AppProvider 返回 nacos 配置源 Provider（config 段）。
func AppProvider() appkit.Provider { return appProvider{} }

type appProvider struct{}

func (appProvider) Segments() []string { return []string{"config"} }

func (p appProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, p) }

// Plan 声明构造期配置层节点：层先于 App 构造；换源需重启——远端层在构造期
// 已订阅，不能原地替换。
func (appProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{{
		Key:      "config",
		Kind:     "config",
		Segments: []string{"config"},
		Phase:    appkit.PhaseConfigure,
		Reload:   appkit.ReloadRestart,
	}}, nil
}

func (appProvider) Build(ctx context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
	l, closer, err := bootstrap.NacosProvider()(ctx, s.Config)
	if err != nil {
		return nil, nil, fmt.Errorf("config source nacos: %w", err)
	}
	var layers []baldconfig.Layer
	if l != nil {
		layers = append(layers, *l)
	}
	var cleanup appkit.Cleanup
	if closer != nil {
		cleanup = func(context.Context) error { closer(); return nil }
	}
	return layers, cleanup, nil
}
