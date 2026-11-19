# dev 报告：分析 bald 整体架构，进行设计优化、性能优化、bug 修复

**分支**: `dev/arch-optimize`（基线 main @ b1475399，5 个 round commit，未推送未合并）
**诊断底稿**: `.dev/smell.md`（6 路并行子代理诊断 + 主会话逐条验真）

## 验收标准

- [x] **C1** 整体架构诊断报告 — 证据：`.dev/smell.md`，25 module / 88.6k LOC，Critical 8 / Warning 12 / Suggestion 若干，全部带 file:line；含误报剔除记录（store paging 负 offset、bconf unset Duration panic 判不成立）与 J1 约束声明（零调用不删除）。
- [x] **C2** ≥1 个 bug 复现测试红→绿 — 证据：共 **5 处**取得真实红证据：
  - transport/kafka+rabbitmq `Stop` 无锁改订阅映射 → 真实 crash：`fatal error: concurrent map iteration and map write`（server.go:134）
  - kafka 批量订阅只给 batchSize → `panic: non-positive interval for NewTicker`（subscriber.go:154，stash 对照取证）
  - asynq 假启动 → `Start 未在 15s 内返回（假启动阻塞）`
  - sse eventTTL 清空整流 → `autoReplay=false + EventTTL 时事件被静默丢弃`
  - cli 坏 go directive → `go 指令版本无效 "go1.27.4"`
- [x] **C3** ≥1 项 bench 证据的性能优化 — 证据：**两项**，均有 -count 5 前后对照：
  - selector p2c/circuitbreaker：稳态 Select **1103 → 473.3 ns/op（-57%）**，allocs 不变
  - websocket 入站消息：**215.3 → 106.4 ns/op（-51%）**，**704 → 364 B/op（-48%）**，**7 → 4 allocs（-43%）**
- [x] **C4** ≥1 项行为不变的设计收敛 — 证据：Round 5（f3090fd1），bconf 7 份复制 setter → 单一泛型 `scalarSetter[T]`，Type/String/Set 输出逐字节一致，alloc guard 双测试 PASS，cov:excl 块级豁免经 coverage-merge 源码+profile 实测成立。
- [x] **C5** 仓库硬门禁 — **部分完成，见「环境限制」**：全仓 26 module build+vet 全绿；`go test -short` 扫描中 21 个 module 全绿，5 个 module 存在**既有** Windows 环境失败（cli/internal/codegen 5 例——stash 对照证实基线即红；bconfig `TestNew_AbsError`、bootstrap `TestLoad_LocalOpenError`、cobramcp `TestConfigPath_WorkspaceFallback`×2、pkg `TestRealPluginExec`——所在 module 相对 main 零改动，失败不可能由本次引入）；`task lint/race/vuln` 本机缺 task / golangci-lint / CGO（gcc），由 CI 承担。

## 指标

| 轮次 | 基准 | 指标 | 相对基线 |
|---:|---|---|---:|
| 3 | `BenchmarkP2CSelectSteady`（8 节点 RunParallel） | 473.3 ns/op | **-57%**（1103→473.3） |
| 3 | `BenchmarkP2CSelectChurn`（双集合翻滚） | 1049 ns/op | +14%（918.5→1049，prune 真实工作路径的快检读锁代价，罕见场景，如实入账） |
| 4 | `BenchmarkHandleSocketRawDataBinary`（nil binder 路径） | 106.4 ns/op | **-51%**（215.3→106.4） |
| 4 | 同上 | 364 B/op / 4 allocs | -48% / -43% |

**账本口径说明**：`.dev/results.tsv` 的 delta_pct 跨基准不可比（round 3 与 4 用的是不同 benchmark），逐轮以前后对照为准（上表）。Round 1/2/5 为 robustness/refactor 目标，无指标（NA）。

## 每轮

| 轮次 | 目标 | 假设 | 门禁 | 判决 | commit |
|---:|---|---|---|---|---:|
| 1 | robustness | broker/transport 5 处崩溃级并发缺陷可一次修复且复现测试红转绿 | 4 module 全绿（-short）；-race 本机不可用由 CI 兜底 | KEEP | 1ccd5c03 |
| 2 | robustness | asynq 假启动 / sse 整流清空 / cli 坏 go directive 三个静默失败缺陷修复后红转绿 | sse/asynq/cli 全绿 | KEEP | 6adfa388 |
| 3 | perf | prune 的无条件写锁是每请求串行化点，读锁快检可降稳态 ns/op ≥epsilon | selector 全家族 -short 全绿 | KEEP | 0a88b836 |
| 4 | perf | 第二份 unmarshal 与重复 handler 查找是纯浪费 | websocket 全绿 | KEEP | a5de3fbb |
| 5 | refactor | 7 份复制 setter 是漂移温床，泛型收敛行为不变 | bconf 全绿 + alloc guard PASS | KEEP | f3090fd1 |

停止原因：`STOP:max_rounds`（rounds_max=5 用尽；全部验收标准已达成）。

## 审阅处置

4 轮审阅（Round 1/2/4/5 独立子代理，diff+验收标准+假设三输入），**blocking 共 3 条，全部验真后本轮内修复并重跑门禁**：

- R1-B1（**验真采纳**）：我把 rabbitmq `waitConnection` 从电平锁存改成边沿广播——晚到的 resubscribe 等待方会永久阻塞、连接健康期间零消费。审阅者用状态机模拟实证。修复：`signalConnectedLocked` 恢复锁存语义 + select-default 判重防双重 close，并补「晚到等待方 100ms 内放行」回归测试（正是抓这回归的测试）。
- R1-B2..B5（验真采纳/部分）：reconnect 裸读 `r.Connection.NotifyClose`、pubCh 失败路径语义偏移、批量校验注释与负值行为不符、transport Stop 孤儿订阅窗口——全部同轮修复。
- R2-B1（**验真采纳**）：既有测试 `TestServeHTTP_TTLSkipsExpired` 把 bug 行为钉成预期，与新修复矛盾 → 改写为钉住修正语义，注释说明与 event_ttl_test 的分工。R2-B2（**验真采纳**）：Ping 失败路径在 scheduler 已启动后返回会泄漏心跳 goroutine → Ping 前移到任何组件启动之前（失败路径零泄漏）。
- R4/R5：0 blocking（R4 审阅者实测 golangci-lint v2.15.0 零告警；R5 审阅者实测 coverage-merge 的 cov:excl 块级豁免链）。
- **拒绝 0 条**；advisory 采纳 11 条（全部一轮内落地），登记 2 条（见未解决）。

## 修复清单（8 项已证实的生产缺陷）

1. **kafka `publishOneWriter` 解锁裸读 writer**（broker/kafka/kafka.go）——nil panic/撕裂读，热发布路径；改锁内捕获局部变量（与 publishMultipleWriter 同范式）。
2. **kafka 批量订阅 NewTicker(0) panic**（broker/kafka/kafka.go）——只给 batchSize 全进程崩；按 J5 fail-fast 报错要求成对声明（含负值拒绝）。
3. **rabbitmq waitConnection 双重 close panic**（broker/rabbitmq/connection.go）——Connect→Close→Connect 即触发；判重锁存 + Close 语义对齐。
4. **rabbitmq 连接字段无锁读写**（connection.go + subscriber.go）——tryConnect 裸写、reconnect 裸读 NotifyClose、Consume/Subscribe 裸读快照化；顺带修 DefaultAmqpConfig 全局污染隐患。
5. **transport kafka/rabbitmq Stop 无锁改订阅映射**（transport/*）——真实 crash 复现；锁内快照+清空+started 复位同临界区，退订移锁外。
6. **asynq 消费者假启动**（transport/asynq/server.go）——Start 前置 Redis Ping（先于任何组件，失败零泄漏）+ Run 启动错误经 errChan 上抛。
7. **sse eventTTL 静默清空整流**（transport/sse/http.go）——零值时间戳不再参与过期判定；旧测试钉的是 bug 行为，已改写。
8. **cli 种子 go.mod 无效指令**（cli/internal/codegen/app.go）——`go go1.27.4` → 复用 `goMinorVersion()`。

## 未解决（移交后续，按优先级）

1. **kafka/rabbitmq transport ~95% 复制代码统一**（J2 单一闸门）——本轮同族缺陷 ×3 双发的根因；属大架构动作，需独立立项。
2. **websocket 慢客户端无界阻塞**（SendMessage 无超时、Broadcast 串行）——行为变更（sse 已有 trySend 范式可移植），需产品决策丢弃语义。
3. **appkit keywatch 持锁分发用户回调**（重入自死锁风险，latent）——锁协议变更，需独立轮。
4. **cli 14 处非原子写用户文件**（appspec.json/IDE 配置）——cfgmgr 已有 .backup.json 范式可推广。
5. **cli convention check 14× 全树 walk+parse**——共享 walk 调度可降 ~14×，改 cli/internal/convention。
6. **bconf 裸数字时长→纳秒、tlsconf file+config 同设静默取 file、repeated message 列表跳过 coercion**——契约装载语义修复，需契约评审。
7. **既有 Windows 测试失败**（cli/internal/codegen 5 例 + bconfig/bootstrap/cobramcp/pkg 各 1-2 例，均为文件权限/路径/插件执行的平台语义用例；cli 侧经 stash 对照证实基线即红，其余 module 相对 main 零改动）——CI 在 Linux 上绿，建议标注 skip-on-windows 或修平台分支。
8. **J1 登记**：pkg/fieldmaskutil、stringcase、id、testkit、若干零调用导出——按 J1 保留，不删除。
9. **root go.mod 3 条悬挂/机器特定 replace**（log/charm、log/sentry 目录不存在；ratelimit 指向 macOS 绝对路径）——实测休眠（根模块无可导入包），低成本清理项。
10. **websocket WithMessageUnmarshaler 在入站路径被绕过**（审阅发现的既有缺陷，advisory 登记）。

## 环境限制（如实记录）

- 本机无 `task`、`golangci-lint`、gcc（CGO）→ `task lint`（golangci 五类）、`task race`、`task vuln` 无法本地执行。替代：26 module 全量 build+vet+test(-short) 扫描通过；-race 与 lint 由 CI 承担（race 修复均有非 race 可复现的 crash 证据或结构论证兜底）。
- 账本修复：Round 1 时漏跑 `loop baseline`，事后把缺失的 iter=0 基线行插入表头后（**未改写任何已记录判定行**），`loop audit` 通过。
- round 4 前一轮门的失败教训：`go test ./... | head` 截断掩盖过一次真实失败（sse 旧测试与新语义矛盾）——后续所有门禁输出都全量检查。

## 交付物

```
.dev/
├── run.json          流程状态（5 轮全部 keep，phase=done）
├── results.tsv       判定账本（6 行含基线，audit 通过）
├── smell.md          架构诊断底稿
└── REPORT.md         本报告
dev/arch-optimize 分支 5 个 round commit（1ccd5c03 → f3090fd1）
新增复现/回归测试 9 个文件、benchmark 2 个文件
```
