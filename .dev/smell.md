# bald 架构诊断报告（smell）

**日期**: 2026-11-16 · **范围**: 全仓 25 module / 88.6k LOC（6 路并行子代理诊断 + 主会话逐条验真）
**判据约束**: 本报告的处置建议受仓库 J1–J9 约束——所有「删除死代码」类发现一律按 **J1（零调用不是删除依据）** 降级为登记，不采纳删除。

---

## 总览

整体架构健康度 **🟢 良好**：模块依赖图无环、无越层级依赖爆炸；全局可变状态 13 处全部有锁或 set-once；测试/源码比 ≈ 0.99；经典 Go 缺陷（WaitGroup 误用、循环 defer、TOCTOU、time.After 泄漏）大面积缺位。

主要问题集中在三处：
1. **broker/transport 家族的并发缺陷**（数据竞争 ×5、panic ×2、静默死亡 ×2）——且 kafka 与 rabbitmq 两份 ~95% 复制代码使同一缺陷必然双发（J2 重复税的实证）。
2. **selector 每-pick 热路径的写锁**（p2c / circuitbreaker prune）——每请求串行化点。
3. **静默失败类缺陷**（asynq 假启动、sse 全丢弃、cli 生成坏 go.mod、bconf 裸数字时长）——违背仓库自身 fail-fast 纪律。

---

## 🔴 Critical（已验证，本轮候选）

| # | 缺陷 | 位置 | 验证 |
|---|---|---|---|
| C-1 | kafka `publishOneWriter` 解锁后裸读 `b.writer.Writer`，与错误路径置 nil 竞争 → nil panic / 撕裂读。`publishMultipleWriter` 已有正确范式（锁内捕获局部变量） | broker/kafka/kafka.go:483-492 vs :507,:515 | ✅ 主会话读码证实 |
| C-2 | kafka 批量订阅 `time.NewTicker(0)` panic：`isBatchMode()` 是「或」语义，只设 `WithSubscribeBatchSize` 时 interval=0 → 后台 goroutine panic 全进程崩 | broker/kafka/subscriber.go:139-154；kafka.go:631-636 | ✅ 主会话读码证实 |
| C-3 | rabbitmq `connect()` 无条件 `close(r.waitConnection)`；通道构造时创建一次、Close 后不重建 → Connect→Close→Connect 二次 close panic；并发 Connect 同样双 close | broker/rabbitmq/connection.go:170,:195；:216-233 | ✅ 主会话读码证实 |
| C-4 | transport kafka/rabbitmq `Stop` 无锁遍历并重建 `s.subscribers`/`s.subscriberOpts`，与持锁的 `RegisterSubscriber` 并发 → fatal concurrent map read/write。同一缺陷在两份复制代码中双发 | transport/kafka/server.go:98-108 vs :119-122；transport/rabbitmq/server.go:99-109 | ✅ 主会话读码证实（两文件逐字相同） |
| C-5 | rabbitmq `tryConnect` 无锁写 `r.Connection`/`r.Channel`，与持锁读的 `Consume`/`lazyInitPublishChannel` 竞争；重连风暴窗口内 Publish 可在旧连接上建通道 | broker/rabbitmq/connection.go:272,:277 vs :312,:377 | ✅ 主会话读码证实 |
| C-6 | asynq `Start` 吞掉消费者 `Run` 失败：goroutine 内仅 log，`Start` 无条件阻塞 `<-ctx.Done()` → Redis 不可达时进程假启动，任务进死消费者；Stop 先于 log 时连日志都丢 | transport/asynq/server.go:363-375 | ✅ 主会话读码证实（对比 sse/websocket 的 errChan 范式） |
| C-7 | sse `eventTTL` 零时间戳全丢弃：timestamp 仅在 autoReplay+hasContent 时由 EventLog.Add 设置；`WithEventTTL` + `autoReplay=false` → `zero.Add(ttl)` 恒过期 → 全部事件静默丢弃 | transport/sse/http.go:111 vs stream.go:92-96 | ✅ 主会话读码证实 |
| C-8 | cli `seedGoMod` 写入 `go go1.27.1`（`runtime.Version()` 带 `go` 前缀）→ 每个新生成树第一次 `go mod tidy` 即失败；同文件已有正确的 `goMinorVersion()`（注释声称两处一致，实际分叉） | cli/internal/codegen/app.go:292 vs appspec_gen.go:94-101 | ✅ 主会话读码证实 |

## 🟡 Warning（已验证或强证据，登记）

| # | 问题 | 位置 | 状态 |
|---|---|---|---|
| W-1 | websocket `SendMessage` 无界阻塞 + `Broadcast` 串行：慢客户端阻塞广播与其余所有会话（sse 已有 trySend 正确范式） | transport/websocket/session.go:86-92; server.go:327-338 | 子代理证实，主会话抽查结构属实 |
| W-2 | websocket 每消息双重 unmarshal + 重复 handler 查找 | transport/websocket/server.go:483-510 | 子代理证实 |
| W-3 | appkit keywatch 持锁分发用户回调 → 回调内重入订阅 API 即自死锁；协调器跑在 `context.Background()` 无预算 | pkg/appkit/keywatch.go:77-93, :65-73 | 子代理证实（当前无重入调用者，latent） |
| W-4 | appkit `_ = fs.Parse` 吞掉畸形 flag 值错误（未知 flag 容忍是刻意的，畸形值容错不是） | pkg/appkit/appkit.go:552-553; serve.go:122,140 | 子代理证实 |
| W-5 | bconf 裸数字时长静默解释为纳秒（YAML 写 `stop_timeout: 30` → 30ns）；flag 通道与文件通道对空串判定相反 | bconf/unmarshal.go:796-803; util.go:48-58 | 子代理证实（单数路径历史上真踩过 "1500ms" 生产 bug） |
| W-6 | bconf tlsconf `file` 与 `config` PEM 同设时静默按 switch 顺序取 file，二选一约束无检测 | bconf/tlsconf/tlsconf.go:96-118 | 子代理证实 |
| W-7 | p2c / cbSelector 每 pick 无条件写锁 prune → 高 QPS 下所有 LB 决策串行化 | selector/p2c/p2c.go:64,:143-148; selector/circuitbreaker/circuitbreaker.go:160,:93 | ✅ 主会话读码证实（p2c 侧） |
| W-8 | retry 默认 `NoJitter`，文档却声称防惊群；etcd/kafka 各自绕过默认用 Full/Equal | retry/retry.go:161 vs :97 | 子代理证实 |
| W-9 | bconf repeated message 列表元素跳过 coercion（含 repeated Duration）——单数路径修过的 bug 在 repeated 路径复活 | bconf/unmarshal.go:516-518 | 子代理证实（当前契约树无 repeated Duration，latent） |
| W-10 | store `uint32(total)` 截断 + offset 大值跨后端行为不一致（负 offset 守卫已存在，需补 uint64→int 溢出分支） | pkg/store/store.go:549; paging.go | ⚠️ 主会话验真时发现 agent 原判「无守卫」不实，仅保留溢出分支疑点 |
| W-11 | root go.mod 3 条悬挂/机器特定 replace：`log/charm`、`log/sentry` 目录不存在，`ratelimit` 指向 `/Users/moweilong/...` 绝对路径。实测 `go list -m all` 通过（根模块无可导入包，休眠地雷）；bconf/bootstrap.go:107 仍文档化 charm/sentry | go.mod:19,:23,:39 | ✅ 主会话实测证实（休眠） |
| W-12 | gin Timeout 中间件每请求 2 倍内存驻留（无界缓冲响应体，最后一次性写出） | pkg/middleware/gin/timeout.go:49-54,:149-156 | 子代理证实 |

## 🔵 Suggestion（登记，不在本轮处理）

- **重复税（J2）**：transport/kafka ≈ transport/rabbitmq（server.go 82%、logger.go 87%、options.go 71%）；cobramcp 双引擎（config.go vs server.go 两套 registerToolsRecursive/cmdFilter）；bconf 7 个复制 flag setter（bindflags.go:171-297）；bconf 双折叠算法（foldkeys.go vs unmarshal.go 口径漂移已现端倪）；broker/kafka vs rabbitmq 发布链路同构。
- **God 文件**：cli checkers.go 1020 行、asynq server.go 965 行、bconf unmarshal.go 876 行、appkit.go 980 行。
- **perf 冷路径**：convention check 14× 全树 walk+parse（O(checkers×files)）；inmemory 每行重编译 REGEXP；sentinel Wait 10ms 忙轮询；release plan O(M²·C·F)。
- **cli 非原子写**：14 处 `os.WriteFile` 原地写用户文件（appspec.json、IDE 配置），无 temp+rename/backup；cfgmgr 已有 `.backup.json` 范式未推广。
- **J1 登记（不删除）**：pkg/fieldmaskutil、pkg/stringcase、pkg/id、pkg/testkit、`UnaryObservability`/`StreamObservability`/`WithUnifiedValidator`/`WithProbes` 等零生产调用导出——按 J1 保留，仅登记。
- **测试缺口**：transport/subscribe 0 测试；broker/rabbitmq 2/12；workflow/argo 2/4；sse 5/11；websocket 5/8。
- **选型余量**：sentinel 双适配器脚手架同构；cli 内 4 份 Streams 结构体。

## 已检查并排除（误报/不成立）

- store paging「负 offset 漏给后端」——实际有 `off < 0` 守卫（agent 误报，保留 W-10 溢出分支疑点）。
- bconf unset Duration flag panic——对 protobuf-go v1.36.12 实测路径不成立（typed nil 处理正确）。
- pkg WaitGroup/双重关闭/transporterPool——全部有 Once/测试固定，无缺陷。
- tokenbucket 数学、p2c EWMA CAS、loadable singleflight、local 条带锁、redis MGET/pipeline——验证干净。
- 模块依赖环——pkg→bootstrap→gateway 一墙之隔但当前无环。

---

## 本轮切片（rounds_max=5）

| 轮 | 目标 | 假设 | 判据 |
|---|---|---|---|
| 1 | robustness | broker/transport 崩溃级并发缺陷（C-1..C-5）可一次修复，新增复现用例红→绿，race 门禁绿 | 复现测试 + go-gate --race |
| 2 | robustness | 静默失败缺陷（C-6 asynq 假启动、C-7 sse 全丢弃、C-8 坏 go directive）修复后复现用例红→绿 | 复现测试 + go-gate |
| 3 | perf | p2c/cbSelector 每-pick 写锁是序列化点，改读锁双检后 BenchmarkSelect ns/op 降 ≥epsilon | bench 基线对比 |
| 4 | perf | websocket 每消息双重 unmarshal + 重复查找可消除，消息处理 ns/op 与 allocs 降 ≥epsilon | bench 基线对比 |
| 5 | refactor | bconf 7 份 flag setter 收敛为单一泛型 setter（J2），行为不变，测试全绿 + alloc 基线不退化 | go-gate + alloc_guard |

**不可在本需求内修（移交后续）**：kafka/rabbitmq transport 复制代码统一（J2 单一闸门，大架构动作）、gin Timeout 缓冲上限（行为变更需产品决策）、keywatch 重入死锁（修法涉及锁协议变更，需独立轮）、cli 非原子写推广、convention check 共享 walk、J1 登记的死代码。

## 建议测量点（perf 轮）

- selector/p2c：`BenchmarkSelect`（若无则新建），节点数 8/64，含 prune 生效（state 超额）与稳态两档。
- websocket：`defaultHandleSocketRawData` 消息处理 bench，典型 packet 512B。
- bconf：现有 `alloc_guard_test.go` 基线即判据。
