# 六域 Registry 迁入 bootstrap

> Title: 六域客户端 Registry（Database/Cache/Storage/Ai/Workflow/Broker）自 pkg/appkit 迁入 bootstrap 子模块
> Author(s): kalandramo
> Last updated: 2026-09-15
> Status: Accepted（对应实现 `bootstrap` module，待发 `bootstrap/v0.7.0`）

## 摘要

评估「bootstrap（插件初始化装配层）与 pkg/appkit（插件生命周期管理层）能否合并
为一个包」，结论是**不能合并**；采纳的折中方案是把 appkit 中仅依赖 bconf 契约
的六个域 Registry 迁入 bootstrap 子模块，对齐既有 `WithConfigRegistry(r
*baldbootstrap.Registry)` 模式——「Registry 在 bootstrap、Option 在 appkit」。

## 为什么不能合并为一个包

1. **模块结构走不通**：bootstrap 是独立子模块（已发 v0.6.0），appkit 属根模块
   且依赖 `pkg/registry`、`pkg/audit`、`pkg/authn`（均在根模块）。
   - appkit 下沉进 bootstrap：bootstrap 必须 require 根模块 → 循环依赖；
   - bootstrap 并入根模块：已发 tag 作废、下游 import breaking。
2. **职责正交**：bootstrap 是构造期（main 里、Run 前，「契约段 → 实例」工厂 +
   注册表 + 短路回滚）；appkit 是运行期编排（五阶段停机、Effect 逆序回放、
   Component、热更新、协调器）。`FromBootstrap` 的 cleanup → Effect 桥接本身
   证明两个阶段需要不同抽象。
3. **依赖闭包差异**：bootstrap 闭包 = 配置中心客户端（kratos/vault/consul…）；
   appkit 闭包 = 根模块全家（gin/otel/audit）。合并后「只要装配不要编排」的
   用户被迫拖整个闭包。

## 迁移边界

每个域文件按同一刀法切分（六文件同构）：

| 归属 | 内容 |
|---|---|
| **迁入 bootstrap**（构造期） | `XxxProvider` 类型、`XxxRegistry` 结构（New/Register/MustRegister）、段枚举（`xxxSections`）、`Build` 方法、Build 行为测试；错误前缀 `appkit:` → `bootstrap:` |
| **留在 appkit**（运行期） | `WithXxxRegistry` Option（签名改 `*baldbootstrap.XxxRegistry`）、`buildXxx` 胶水（阶段 B 调 Build、实例存 AppKit 字段）、`AppKit.Xxx()/Xxxs()` 访问器、FromBootstrap 集成测试；胶水层错误保持 `appkit:` |

**不迁**（依赖根模块包，迁入即循环依赖）：

- `RegistrarRegistry`（依赖 `pkg/registry`）
- `TracerRegistry` / `MetricsRegistry`（依赖 otel）

**新插件初始化归位判别规则**（registry.go 包注释同步登记）：

- 产物仅是「契约段 → 实例」的构造（仅依赖 bconf/标准库）→ Registry 放 bootstrap；
- 产物需进入运行期编排（依赖根模块包、挂 Effect/Component、存 AppKit 字段）→
  注册表放 pkg/appkit。

## 实现清单

- 新增 `bootstrap/{database,cache,storage,ai,workflow,broker}.go` 六文件 +
  对应 `_test.go`（Build 行为测试 14 个：注册 fail-fast/段枚举/多段并存/逆序回放/
  失败回滚）。
- 瘦身 `pkg/appkit/` 同名六文件与 `_test.go`（FromBootstrap 集成测试 14 个留此，
  构造 Registry 处改 `baldbootstrap.NewXxxRegistry()`）；`bootstrap.go` 的
  `bootstrapSpec` 六字段类型同步改 `*baldbootstrap.XxxRegistry`。
- 16 个 contract 包注释更新（`appkit.NewXxxRegistry` → `bootstrap.NewXxxRegistry`、
  `appkit.XxxProvider` → `bootstrap.XxxProvider`）：contrib/database/{gorm,mongodb}、
  cache/{redis,local}、oss/{minio,s3}、ai/{openai,langchaingo,eino}、
  workflow/argo、broker/{kafka,rabbitmq,redis,rocketmq}。contract 包的类型守卫
  本就是结构化断言（不 import appkit），零代码级变更。
- 文档定点更新：《框架契约总览.md》（六行表格）、《模块依赖关系.md》（bootstrap
  职责 + 归位规则 + 日期）、《Bald Bootstrap 设计.md》（摘要/总览表格/归属分界
  段）、《AppKit FromBootstrap 约定装配.md》（用法示例）。《数据存储设计.md》
  核实后仅引用不迁的 `appkit.RegistrarRegistry`，无需修改。

## 依赖与影响

- **零新增模块依赖**：六域文件 import 仅 `bootstrapv1`（bconf 生成物）+ 标准库，
  bootstrap 子模块已 require bconf v0.5.0，go.sum 无变化。
- **`_example` 零使用**六域 Registry（已搜索确认）。
- **API 变化**：`appkit.NewXxxRegistry` → `bootstrap.NewXxxRegistry`（下游
  bald-admin main.go 需适配，见下）。

## 验证

- bootstrap 子模块：`go build` / `go vet` 通过；六域 Registry 测试 14 个全 PASS。
- 根模块：`go build ./...` 通过；appkit 六域 FromBootstrap 集成测试 14 个全 PASS。
- 存量失败（与本次无关，改动前即存在）：bootstrap 3 个 + appkit 2 个日志测试，
  均为 Windows 下 lumberjack 文件句柄延迟释放导致 `t.TempDir()` 清理失败，
  断言本身通过，测试文件不在本次改动列表。

## 发布链（顺序不可倒置，待用户确认）

1. bald 仓库迁移完成（本文件对应实现）；
2. 用户确认提交、打 `bootstrap/v0.7.0` tag；
3. 根模块 go.mod 兄弟 require 升真实版本 `v0.6.0 → v0.7.0`（replace 保留本地
   开发，proxy 忽略 replace）；
4. bald-admin 适配：main.go 的 `NewDatabaseRegistry/NewCacheRegistry/
   NewStorageRegistry` 构造改 bootstrap 包（`With*Registry` 调用不变）、
   go.mod 显式 require bootstrap v0.7.0；tag 未经 proxy 收录时用
   `GONOSUMDB='github.com/kalandramo/bald,...'` 绕过 404。

## 相关文档

- 《Bald Bootstrap 设计.md》——bootstrap 模块专文（本次更新摘要/总览/归属分界）
- 《模块依赖关系.md》——依赖方向实证（本次更新 bootstrap 职责与归位规则）
- 《AppKit FromBootstrap 约定装配.md》——应用侧约定装配（本次更新用法示例）
