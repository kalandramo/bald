// keywatch.go 实现 R1 配置增量协调的第一子集：key 级变更订阅
// （docs/devel/zh-CN/架构优化路线.md，对照 Cordis 组件加载器的增量协调）。
//
// 背景：appkit.OnConfigChange 是全量回调——配置任何变更都触发，业务在其中
// 重新 Unmarshal 完成热重载。多数热更新场景只关心个别 key（日志级别、监听
// 地址、auditor 后端），全量回调把「哪些变了」的 diff 责任推给每个业务。
// OnKeyChange 把 diff 下沉到框架：只有订阅的 key 新旧值确实不同才触发：
//
//	app := appkit.New(
//	    appkit.OnKeyChange("http.addr", func(old, new string) {
//	        log.Info(ctx, "http.addr changed", "old", old, "new", new)
//	    }),
//	    appkit.OnConfigChange(func(v *viper.Viper) { /* 全量重载，仍可用 */ }),
//	    ...
//	)
//
// 语义（由 keywatch_test.go 固化）：
//   - 启动时以首次加载值为基线（首次变更前不触发）；
//   - 仅当新值 != 旧值才触发（同值刷新不触发）；
//   - fn 与 OnConfigChange 共存：先跑全量回调，再分发 key 订阅；
//   - 未开启 watch（WatchLocalFile/Remote 均未配置）时永不触发。
//
// 并发语义与 OnConfigChange 相同：分发发生在 pkg/config 的变更回调内（其 viper
// 注入已完成）。fn 应轻量（改状态、切引用、记日志），不要在其中调用 config
// 重载/注入类操作或长时间阻塞。
package appkit

import (
	"github.com/spf13/viper"
)

// keyWatcher 记录一个 key 级订阅：fn 是回调，last 是上次观测值，armed 表示
// 已建立基线（首次加载后置位，未武装前只建快照不触发）。
type keyWatcher struct {
	key   string
	fn    func(old, new string)
	last  string
	armed bool
}

// OnKeyChange 订阅单个配置 key 的变更（R1 增量协调）。仅当该 key 的新值与
// 上次观测值不同才触发 fn(old, new)——同值刷新不触发，未订阅的 key 变更
// 不波及。可与 OnConfigChange 叠加使用。key 语法同 viper（"http.addr"）。
func OnKeyChange(key string, fn func(old, new string)) Option {
	return func(a *AppKit) {
		a.keyWatchers = append(a.keyWatchers, keyWatcher{key: key, fn: fn})
	}
}

// armKeyWatchers 在配置首次加载完成后为全部订阅建立基线快照。
func (a *AppKit) armKeyWatchers() {
	a.keyWatchMu.Lock()
	defer a.keyWatchMu.Unlock()
	for i := range a.keyWatchers {
		w := &a.keyWatchers[i]
		w.last = a.cfg.v.GetString(w.key)
		w.armed = true
	}
}

// wrapKeyWatch 包装用户全量回调：先跑全量（保留既有行为），再分发 key 订阅。
func (a *AppKit) wrapKeyWatch(user func(*viper.Viper)) func(*viper.Viper) {
	return func(v *viper.Viper) {
		if user != nil {
			user(v)
		}
		a.dispatchKeyWatch(v)
	}
}

// dispatchKeyWatch 逐 key 比对新值，仅对确实变化的订阅触发 fn。
// 锁内串行分发：保证同一 watcher 的 fn 不并发、观测顺序与变更顺序一致。
func (a *AppKit) dispatchKeyWatch(v *viper.Viper) {
	a.keyWatchMu.Lock()
	defer a.keyWatchMu.Unlock()
	for i := range a.keyWatchers {
		w := &a.keyWatchers[i]
		if !w.armed {
			continue // 未武装（loadConfig 未完成）不触发
		}
		cur := v.GetString(w.key)
		if cur == w.last {
			continue
		}
		old := w.last
		w.last = cur
		w.fn(old, cur)
	}
}
