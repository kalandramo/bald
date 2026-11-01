package appkit

import "time"

// defaultStopTimeout 是优雅停机总预算的代码层兜底（NewApp 无契约路径，可经
// StopTimeout Option 覆盖）。Serve 路径由契约 app.stop_timeout 覆盖它
// （bconf 默认 30s）。
const defaultStopTimeout = 10 * time.Second

// fallbackPhaseBudget 是从 stopTimeout 派生不出预算时（显式 0）的兜底。
const fallbackPhaseBudget = 5 * time.Second

// phaseBudgetFor 返回单个生命周期阶段的时间预算：取 stopTimeout 的一半，
// 总停机时间不随阶段数线性膨胀。
//
// 端点等待、before/afterStop 钩子、效应回放、组件 Start/Dispose 共用这一派生
// 值，可调入口因此只有 app.stop_timeout（契约）/ StopTimeout Option 一个。此前
// 这些阶段各有一个 5s 常量封顶：慢监听（需初始化大量连接的 broker transport）
// 会中止启动而无调整入口，慢钩子同理，且调大 stop_timeout 也救不了它们。
func phaseBudgetFor(stopTimeout time.Duration) time.Duration {
	return orDuration(stopTimeout/2, fallbackPhaseBudget)
}
