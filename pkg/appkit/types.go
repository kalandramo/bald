package appkit

import "time"

// 默认优雅停机超时。
const defaultStopTimeout = 10 * time.Second

// 钩子阶段默认超时为 stopTimeout 的一半，避免总停机时间线性膨胀。
const defaultHookTimeout = 5 * time.Second
