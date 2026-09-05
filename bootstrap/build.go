package bootstrap

import (
	"context"
	"errors"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bconfig"
	"github.com/kalandramo/bald/bootstrap/config"
)

// Build 按注册序装配命名配置层。
//
// 遍历所有已注册的 provider，依序调用之：
//   - 返回 error：立即短路，回滚（关闭）已构造的源，返回包装后的错误；
//   - 返回 nil layer：该源未在契约中配置，跳过；
//   - layer.Name 为空时填注册名；Watch=true 但 Reader 不实现
//     bconfig.ValueWatcher 时报错（fail-fast，早于 Store 装配）。
//
// 全部完成后返回层列表——注册序即优先级：列表首元素最高（先注册的源覆盖
// 后注册的），与 [config.Store] 的合并约定一致（从列表尾向头叠加）。
//
// 返回的 cleanup 逆序释放各 provider 资源（后构造的先释放，与装配相反）；
// 装配失败时 Build 内部已回滚，此时 cleanup 为 nil。层 reader 的资源归
// cleanup 释放，Store.Close 不重复关闭。
func (r *Registry) Build(ctx context.Context, cfg *bootstrapv1.BootstrapConfig) ([]config.Layer, func(), error) {
	if cfg == nil {
		return nil, nil, errors.New("bootstrap: config is nil")
	}

	names, providers := r.snapshot()

	var (
		layers  []config.Layer
		closers []func()
	)
	for _, name := range names {
		l, closer, err := providers[name](ctx, cfg)
		if err != nil {
			runClosers(closers)
			return nil, nil, fmt.Errorf("bootstrap: provider %s: %w", name, err)
		}
		if l == nil {
			continue
		}
		if l.Reader == nil {
			runClosers(closers)
			return nil, nil, fmt.Errorf("bootstrap: provider %s: layer Reader is nil", name)
		}
		if l.Watch {
			if _, ok := l.Reader.(bconfig.ValueWatcher); !ok {
				runClosers(closers)
				return nil, nil, fmt.Errorf("bootstrap: provider %s: Watch=true but Reader does not implement bconfig.ValueWatcher", name)
			}
		}
		if l.Name == "" {
			l.Name = name
		}
		layers = append(layers, *l)
		if closer != nil {
			closers = append(closers, closer)
		}
	}
	if len(layers) == 0 {
		return nil, nil, errors.New("bootstrap: no config source configured")
	}

	return layers, func() { runClosers(closers) }, nil
}

// runClosers 逆序执行清理函数：后构造的资源先释放（栈式释放）。
// 单个 closer 的错误不阻断其余清理——失败路径的清理宁可带伤走完，也不中途泄漏。
func runClosers(closers []func()) {
	for i := len(closers) - 1; i >= 0; i-- {
		closers[i]()
	}
}
