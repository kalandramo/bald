# bald 架构优化——跨设备开发交接（2026-11-17）

> 本文件在 .dev/ 下不入库；完整内容同时在本机对话记录中，拷走任一即可。
> 远端已推：`origin/main @ f13309eb`。**本地 main @ 185d29a0（项 5）领先 3 提交未推**：
> 推送后 CI 补跑 lint/race/vuln + Linux bench 复测。
> 离线兜底 bundle：`D:\code\bald-handover.bundle`（50MB，含 main + WIP 分支，
> `git clone bald-handover.bundle` 即可重建）。

## 任务背景

原始需求：分析 bald 整体架构（25 module / 88.6k LOC）→ 设计优化、性能优化、bug 修复。
第一阶段（dev 流程 5 轮循环）产出诊断报告 + 8 项缺陷修复 + 2 项性能优化 + 1 项重构，
用户认可后指令：**逐项修复未解决清单并合并到 main**。未解决清单共 10 项（见
`.dev/smell.md` 尾部，该文件已入库随 repo 走）。

## 进度总览：10 / 10 项完成（项 1 本地未推）

| 项 | 内容 | 状态 | merge commit |
|---|---|---|---|
| 9 | root go.mod 悬挂/机器特定 replace 清理 | ✅ 已合并 | 785b39e4 |
| 7 | Windows 测试失败 ×9 + 2 个平台假阳性生产缺陷 | ✅ 已合并 | ac8e4d27 |
| 10 | websocket WithMessageUnmarshaler 入站被绕过 | ✅ 已合并 | 483936c5 |
| 3 | appkit keywatch 持锁分发回调（重入死锁） | ✅ 已合并 | 05d54f50 |
| 2 | websocket 慢客户端无界阻塞（超时踢除） | ✅ 已合并 | 282d3641 |
| 6 | bconf fail-closed ×3（裸数字时长/tlsconf 二选一/repeated Duration） | ✅ 已合并 | 963de3f9 |
| 4 | cli 13 处文件写原子化（fsatomic 包） | ✅ 已合并 | ab2bdaf5 |
| — | 第一阶段 5 轮主成果（并发缺陷/perf/refactor） | ✅ 已合并 | ec1db987 |
| 5 | convention check 共享 walk | ✅ 已合并（2026-11-17 本机） | 185d29a0 |
| 1 | kafka/rabbitmq transport 复制代码统一 | ✅ 已完成（本地未推） | 2b7c3620 |

## 已完成：项 5（2026-11-17 本机合并，main @ 185d29a0，**未推送**）

`perf(convention)` dcb39e6b + `style` cb6e0222（fsatomic gofmt，随分支进 main）
→ merge 185d29a0，分支已删。

- 独立子代理审阅（双二进制运行实证）抓出 1 个 BLOCKING：logging 豁免过滤被提前到
  scanned++ 之前，全豁免模块 ran→skip、`--min-coverage` 门退出码可翻转——已修
  （豁免挪回计数之后）并补回归测试 `TestExemptCountTowardScanned` 钉住；
- NON-BLOCKING 全修：checkAST 豁免口径恢复旧语义（selectFiles 去掉 exempt 参数）、
  bench 路径 `../..` 误指 cli module → 改 `../../..` + go.mod 守卫、bench 挪入
  `walk_test.go`（testing 不再进生产构建）、删 goTree.dir 死字段、无 go.mod 早退、
  缓存键 Windows 大小写双键注释声明；
- **等价**：新旧二进制（f13309eb vs dcb39e6b）仓库根 check 输出逐字节一致
  （13 ran / 0 findings / exit 0）；
- **性能**：仓库根墙钟 800ms→220ms（3.7 倍）。此前「65ms 无差异」是 bench 路径
  误指 cli module 得出的错误结论，提交信息已改写；
- 本地门禁：build/vet/gofmt/cli 全 module `test -short`/docscheck 全绿；
  lint/race/vuln 与 Linux bench 复测留 CI（推送后补跑）。

## 未开始：项 1（kafka/rabbitmq transport 复制代码统一）——最大项

> **✅ 2026-11-18 完结**（本地 main @ 2b7c3620 未推）：片1 brokertype kernel（生命周期/
> 订阅簿/计量/日志，9d1d9c49）+ 片2 brokercontract 契约序列 kernel（2b7c3620）。
> dup-clone 496→313（−37%），五类家族同构逻辑单份化；双审阅通过（片1 零漂移、
> 片2 文案漂移已修+直测表）。详见 .dev/REPORT.md 与 .dev/loop 账本。
> 注意：与 AR 批次会话曾相撞（89794545），已 cherry-pick 序列化收尾；多会话规程见记忆 concurrent-sessions。

- 背景：transport/kafka 与 transport/rabbitmq server 层 ~95% 复制（server.go 82%、
  logger.go 87%、options.go 71%），contract/provider.go 家族克隆 63-77%；本轮修复的
  Stop 无锁 bug 就是同族缺陷双发的实证。broker/kafka vs broker/rabbitmq 发布链路同构。
- 建议路径：新建共享 kernel（如 `transport/internal/brokertype`：Server 生命周期 +
  订阅映射 + wrapHandler，参数化 `broker.Broker` 与端点前缀/naming 规则），两个薄壳
  保留各自 options/kind；contract 层同理抽 family 公共 adapter。属 J2 单一闸门动作，
  建议独立需求、独立分支，迁移期保 `task example-bald-vet` / `task acceptance` 绿。
- 风险点：manifest/生成物门禁（改 transport 会波及生成树与 docs，AGENTS.md 硬门禁
  表里 example-bald-vet/acceptance/docscheck 都要跑）。

## CI 修复线（2026-11-18 完结，已推送 main @ 984a9655，CI #649 **全绿**）

发现 main CI 自 #644（ab2bdaf5）起连续红（最后绿 = #643 b147539946），#648（项 5）
未引入新失败（判红模块集与 #646/#647 完全一致）。三个根因全部修复，#649 九个
job（test/race/example/proto-contract/lint/vuln/acceptance/integration/coverage）
全部通过——coverage 此前一直被跳过，也回来了：

| 根因 | 证据 | 状态 |
|---|---|---|
| ① ./bootstrap 判红：项 6 落地裸数字时长 fail-closed（bootstrap replace ../bconf 吃本地源码），旧测试「纳秒整数」仍钉宽容接受 | 本机可复现红→绿；审阅：可直接合并 + bconf 自测补 uint/float32 穷举 | ✅ 已合并 39de05c1（5416f5bd） |
| ② proto-contract/example 判红：a41c961e 升 buf.version 1.72.0 漏改 buf.sha256 表（仍是 v1.57.2 哈希） | 审阅三重核验：release API digest + 上游 sha256.txt 双源逐字 + 实下载端到端证明 digest 语义；buf 1.72.0 实跑 TestGeneratedMatchesProto 字节零漂移 | ✅ 已合并 aa38ec9e（0c26e3e1） |
| ③ ./cli 判红（仅 Linux，test+race）：**元凶已定位并修复**——0b36b04d（项 7）给 TestWriteGuarded_StatErrors 加的「目标不得被写入」断言用了 `os.IsNotExist`，Windows 语义过、Linux（stat 穿过普通文件报 ENOTDIR）必红；用户贴 run #647 日志第 1208 行实证，审阅双平台语义核实（Go 源码 + Windows 实机），fsatomic 无辜（Linux CI 全绿） | `statErr == nil` 才判红的平台无关不变量；cli codegen 全绿 | ✅ 已合并 984a9655（d7edaafc），**CI #649 实证转绿** |

经验：项 6 合并时只重跑了 bconf module——bootstrap 经 replace 隐式依赖 bconf
本地源码，改动契约实现时**所有带 ../bconf replace 的 module 都要重跑**；
升级 buf.version 的同一提交必须同步换 buf.sha256 两行（已写进表头注释）；
平台语义断言（errno/IsNotExist/路径形态）只在写它的那台 OS 上验证过就会在
另一侧必红——0b36b04d 即此，**双平台断言要么判型优先要么按平台分支**。

## 收尾清单（项 1 完成后）

1. 更新 `.dev/REPORT.md`（注意：REPORT.md 被 gitignore，只在本机；跨设备后重建，
   数据源=本文件 + `.dev/results.tsv`（已入库）+ smell.md「本轮切片」节）；
2. 全仓终验：各 module `go test -short ./...`（26 module，逐 module 目录内跑）；
3. CI 必跑：`task lint / task race / task vuln`（本机无 task、golangci-lint、gcc/CGO
   → -race 本地不可用，全部依赖 CI；race 类修复已有非 race 崩溃证据兜底）；
4. 发布走 `bald release`，不手工打 tag。

## 新设备环境提示

- 若新设备是 Linux：本机记忆里的 Windows 限制大多消失（gcc/CGO、task、golangci-lint），
  `-race` 可用；项 5 的 bench 建议在 Linux 跑。
- 若仍是 Windows：无 gcc → `go test -race` 报 `cgo: C compiler "gcc" not found`（用
  `-count 5` 加压让 runtime 自带的 concurrent map 检测兜底）；task/golangci-lint 未装
  （本地等价：逐 module `go build ./... && go vet ./... && go test -short ./...`，
  module 清单 `scripts/list-modules.sh` 派生）。
- 测试输出**不要用 `| head` 截断**——曾掩盖过一次真实失败（sse 旧测试钉住 bug 行为）。
- 仓库门禁口径是 `-short`（跳手工集成用例）；`.dev/` 内 markdown 不写仓库根视角的
  markdown 链接（docscheck 按文件所在目录解析相对链接，会误报破链）。

## 流程约定（本次全程使用的节奏）

- 每项独立分支 `dev/<slug>` → 修复（**先写复现测试取证 RED**，再修，GREEN）→
  受影响 module 全量测试 → **独立子代理审阅 diff**（blocking 逐条验真后修，修完重跑
  门禁）→ `git checkout main && git merge --no-ff` → 删分支；
- 提交：Conventional Commits + 中文 subject；
- 判据遵循 AGENTS.md J1–J9（零调用不删除=J1、重复税抽单一闸门=J2、显式声明不推断=J5、
  未实现/矛盾配置 fail-fast=J7）；
- loop 账本（若继续用 dev 技能）：`loop keep` 前必须先 `loop baseline`，否则审计拒绝
  （本次踩过，已修账本）。

## 关键数字（第一阶段成果，已随 ec1db987 入库）

- selector p2c/circuitbreaker 稳态 Select：1103→473.3 ns/op（-57%），churn +14%（如实入账）
- websocket 每消息：215.3→106.4 ns/op（-51%）、704→364 B/op、7→4 allocs
- 修复已证实生产缺陷累计 **14 项**（第一阶段 8 + 交接阶段 6），全部带红→绿证据
  （仅 websocket 慢客户端一项因测试引用新字段无法在旧代码编译，以结构论证+看门狗测试钉住）
