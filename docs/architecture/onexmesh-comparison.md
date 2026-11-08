# onexmesh 设计盘点 × bald 对照

> 评审日期：2026-11-08。对象：`../onexmesh`（OneXMesh，单 module Go 微服务运行时框架，13 commits / ~23k 行非测试代码）。
> 信息源：README.md + DESIGN.md（22.6KB）+ 全仓实盘清点（12 维度，证据见文中路径）。
> 本文回答：onexmesh 的设计是什么、它与 bald 在同一问题域上的**哲学分歧**在哪、双方各自值得借鉴什么。

---

## 1. 一句话定位

| | onexmesh | bald |
|---|---|---|
| 是什么 | **运行时框架**：import 即用，业务代码在框架的抽象里跑 | **装配框架**：声明驱动生成 + 装配期收口，业务只写业务 |
| 哲学 | 运行时统一——Transporter 抹平双协议，一份 Handler 同时服务 gRPC/HTTP | 装配期统一——AppSpec 是唯一真相源，生成树是声明的函数，`--check` 零差异 |
| 同类比 | kratos（transport/middleware 抽象）+ go-zero（p2c/降载/SRE 熔断）+ go-micro（config/codec/broker）+ dapr（声明式 resiliency）+ client-go（SDK 生成器） | 无直接同类（声明式装配 + 装配等级 L1–L5 是自创口径） |
| 血统 | onex 体系：复用 onexstack errorsx/token/app，README 锚 miniblog | 同体系但自建：bconf/berrors/bootstrap/appkit 全部自有，只借思想不借实现 |

两者都在孔令飞/OneX 体系内，**是同一问题域（Go 微服务基建）的两条路线**：onexmesh 代表 kratos 式「运行时抽象」路线，bald 代表「声明式装配」路线。

## 2. 逐维度对照

| 维度 | onexmesh | bald | 判断 |
|---|---|---|---|
| **装配** | 代码式组合根 `server.NewMeshServer(...)` + `opts.Mesh.Protocol=grpc\|http\|both`；8 处「工厂注册表 + init() 自注册 + 按名创建」习语贯穿（registry/selector/middleware/codec/config source/broker/HTTP 路由/每端点熔断） | AppSpec 声明 → gen 三道门 → appkit 段驱动 Provider → plan/doctor/drift/reconcile；**刻意禁止 init 自注册**（logging-r09/broker-r07/bootstrap-r01：Provider 的 import 就是依赖清单） | **本质分歧①**：onexmesh 用副作用换便利，bald 用显式声明换可审计——同一问题的对立答案 |
| **传输** | `Transporter` 接口抹平双协议；`NewMethod[Req,Resp]` 泛型把强类型函数擦成统一 Handler；`protoc-gen-onexmesh` 读方法级 `onexmesh.v1.http` 注解生成 `New<Service>Service` 工厂 | transport.Server 契约（Start/Stop/Endpoint）+ 19 个后端独立 module；gen proto 六件套 + buf.validate；proto 是 API 单一真相源 | onexmesh 的「一份业务函数双协议」桥更极致；bald 的「proto 单源 + 生成门禁」更硬。两者其实共享 IDL 驱动思想，落点不同 |
| **错误** | 直接复用 onexstack errorsx + errno 哨兵 + `RenderError` JSON 包络 + protoc-gen-errno（enum→Reason 构造器） | berrors 传输中立自建：`google.rpc.Status` JSON 三面一份（gRPC/转码/gin），httperr.StatusCode 包函数防循环依赖 | 同源异构：onexmesh 押 onexstack 复用，bald 押自建收口（berrors-r08 vs onexmesh 的 `{code,reason}` 包络——语义目标一致） |
| **注册发现** | 7 后端全真实现（polaris/etcd/k8s/consul/nacos/eureka/static）+ **cache 缓存层**（singleflight + 服务/节点双 TTL + stale-while-error + 刷新限流 + 可注入时钟）；etcd 有 lease 丢失自动重注册 monitor | registry 5 后端（inmemory 内置）+ 契约 Provider 装配；**无缓存层** | onexmesh 深；bald 简但 fail-fast。缓存层是 bald 的真空白 |
| **配置** | Source→Loader→Reader→Value 四层抽象 + 注册表；source 仅 file/polaris；热更新到 `Config.Watch` 回调为止（未见 re-validate 联动） | bconf proto 单一真相源 + bconfig 多源 + bootstrap 层模型 + StrictContract 未知键报错 + env 折叠 + 热更新全量重合并 + 判据 J7/J9 fail-fast | bald 的配置是「契约校验型」（错配置启动期死），onexmesh 是「抽象分层型」（错配置静默走默认）——bald-r06/bootstrap-r06 vs onexmesh 的 Source 注册表灵活性 |
| **韧性/限流** | resilience 全家桶（SRE 熔断/降载/重试/**对冲**/舱壁）+ Dapr 风格声明式 resiliency YAML + selector p2c（EWMA）+ gRPC balancer 适配 + 分布式限流 Store（redis Lua） | retry/circuitbreaker（四实现）/ratelimit（三实现）各自独立 module + mech 高级机制面；**无对冲、无 selector/LB 层、无分布式限流 Store** | onexmesh 全；bald 深但面窄。缺口清单见 §4 |
| **可观测** | OTel 三信号 + 五态导出（otel/file/console/classic/hybrid）+ otelslog TraceIDHandler + semconv 集中管理（operation 取路由模板防基数爆炸） | log 契约层/bslog + metrics 三原语两套体系（请求级 semconv vs 传输级）+ observability-otlp + health 三态 | 各有亮点：onexmesh 的 slog→OTel log 桥和 semconv 集中文件值得参考；bald 的两套指标分工（observability-r07）更明确 |
| **代码生成** | 三个 protoc 插件（HTTP 路由 / **client-go 风格 clientset+fake+informer 生成器** / errno）+ golden test | bald gen 四命令 + 三道门 + `--check` 零差异 + PROJECT 账本 | bald 的门禁纪律强；onexmesh 的 clientset 生成器是 bald 没有的能力面（client SDK 生态） |
| **工程纪律** | Makefile 8 target、无 CI、无 e2e 套件、lint 最小集、无 docs/（全押 DESIGN.md）、grpc 被 replace 钉死 v1.64.0（Polaris 兼容债） | 42+ Taskfile target、CI 九作业（含 kind 真集群）、race 全量、docscheck、风险登记册、release 工具链、22 module 无钉版债 | **本质分歧②**：这是 bald 目前最强的领先面 |
| **AI 化** | 无（CLI doctor 偏 demo） | 22 张规范卡 + MCP（4 只读工具）+ ide init 五工具 + gen preset + AGENTS.md/llms.txt | bald 独有面 |

## 3. 三个值得写下来的深层观察

1. **init 自注册 vs Provider 显式装配，是两条路线的分水岭。** onexmesh 的 8 处注册表习语（`RegisterBackend`/`RegisterSource`/`middleware.Register`…）让「引入即生效」，代价是依赖关系藏在 blank import 里——这恰好撞上 bald 的三条判据（logging-r09 禁 init 自注册「Provider 里的 import 就是依赖清单」、bootstrap-r01 禁隐式全局注册、J9 禁链外取值）。**不是谁对谁错**：onexmesh 优化的是插件作者体验，bald 优化的是装配可审计与 fail-fast。若两边要靠拢，中间态是 onexmesh 的 `Backend.AddFlags + Decode`（自描述后端，比纯 init 多了显式声明面）。

2. **「错误配置怎么办」暴露了两种框架观。** onexmesh：Source 缺失/字段缺失 → 静默走默认（灵活性优先）；bald：契约有段无消费者 → 启动期死（J7）、未知键报错（StrictContract）、链外取值对账（J9）。bald 的立场源自「配置说了不算是最危险的缺陷」，onexmesh 的立场源自云原生框架惯例（kratos 同款）。对同一用户群，bald 的选择更贵但更可诊断——doctor/unconsumed 分组就是这套立场的产物。

3. **onexmesh 的 clientset 生成器与 bald 的 gen 互补。** onexmesh 能从资源型 IDL 生成 client-go 风格 SDK（clientset/scheme/fake/informers），bald 生成的是服务端骨架。二者若在同一体系内，恰好是「服务端生成 + 客户端生成」的两半——但 onexmesh 用 `rest/v1` 注解、bald 用 bald.store.v1/CRUD 六件套，IDL 契约不通，短期没有合流点。

## 4. 彼此可借鉴清单

**onexmesh → bald（真空白，按价值排序）**：

1. **registry/cache 发现缓存层**（`pkg/registry/cache`，222 行）：singleflight 去重 + 服务/节点双 TTL + stale-while-error 降级 + MinRetryInterval 刷新限流 + 可注入时钟——bald 的 registry 直连 SDK 后端没有这层，`store-r09` 式的发现抖动保护是缺的。
2. **selector p2c + gRPC balancer 适配**（EWMA 衰减 + 成功率 throttle + address Attributes 传递策略）：bald 无 LB 层，客户端侧负载均衡完全缺位。
3. **resilience 的对冲（hedge）与舱壁（bulkhead）**：bald retry/circuitbreaker 之外的两个正交韧性维度；Dapr 风格声明式 resiliency YAML 也值得 mech 面评估（与 appkit mech 的显式机制注册哲学需要调和）。
4. **otelslog 的 slog→OTel log 桥 + TraceIDHandler**：bald log 的 OTel Logs 桥接（log/bslog/otel.go）可以对照它的 record 转换完整度。
5. **semconv 集中管理文件**（pkg/middleware/semconv.go）：bald 的 semconv 散在 metrics/web 中间件里，集中一处便于对账（对照 observability-r09 的命名纪律）。

**bald → onexmesh（按它自己的缺口排序）**：

1. CI（哪怕先只有 test+lint 两作业）+ e2e：它的后端实现 etcd/polaris/consul/nacos/eureka/redis-store 全部零测试——registry 后端恰是「本地绿远端不一定绿」的高危面。
2. release 工具链与多 module 拆分路径：grpc 钉死 v1.64.0 的 Polaris 债，拆 module 是唯一解法（bald 的零 replace CLI 先例可参考）。
3. 装配可诊断性：doctor 式 `--plan`（onexmesh 的 MeshServer build 链出错时只有 stack，无计划视图）。
4. convention/MCP AI 通道：22 卡模式可平移（它的 8 处注册表习语本身就是 8 张卡的素材）。
5. docscheck 式文档闸门：DESIGN.md 的目录树已与 `pkg/broker` 漂移——机器闸门能拦住这类腐烂。

## 5. 关系判断与建议

- **不竞争**：onexmesh 是「单服务运行时」，bald 是「装配与治理壳」。同一体系内它们甚至服务不同受众——onexmesh 面向「想快速写一个双协议服务的团队」（kratos 受众），bald 面向「想把装配工程化、AI 化的框架作者」（自创受众）。
- **短期建议**：onexmesh 优先补 CI 与后端测试（它的 README 承诺了 7 后端，远端无验证）；bald 评估吸收 registry/cache 与 selector 两块（契约 Provider 化后进 contrib 或能力 module，不破零 replace）。
- **中期悬念**：两条路线是否合流。合流点只有一个方向可行——onexmesh 的 MeshServer 作为 bald `transport.Server` 契约的一个后端（或 appkit 的一个 Provider），前提是 onexmesh 把 `opts.Mesh.Protocol` 的装配决策外移成可声明的段。这是 owner 级裁决，本文只记录形状。

## 6. 移植评估（2026-11-08 增补：要不要复刻它的代码实现）

**前提事实**：onexmesh 是 MIT 许可且远端为同一 owner（`kalandramo/onexmesh`）——法律与归属零障碍，这是自有资产合并。但**整仓复刻不可行也不划算**：它单 module ~23k 行里约六成与 bald 正面重叠（transport/middleware/registry/config/errors 各有对应物），且五个结构性冲突无法绕过：

| 冲突面 | onexmesh 形状 | bald 形状 | 整仓复刻的后果 |
|---|---|---|---|
| 仓库结构 | 单 module + Makefile | 22+ module、零 replace CLI、docscheck 钉包集 | 并入 = 重拆 module + 全部过闸 |
| 注册习语 | 8 处 `RegisterXxx` + init() 自注册 | 判据明令禁止（logging-r09/broker-r07/bootstrap-r01） | 每个包都要改写成 contract/ + Provider 模式 |
| 错误体系 | 直接依赖 onexstack errorsx（12 文件） | berrors 自建；**bald 全仓零 onexstack 依赖** | 移植即引入外部框架依赖，或逐点改写错误构造 |
| 传输抽象 | Transporter 统一抹平 | J6：协议只承诺形状，19 后端独立 | transport/middleware/server 三件套与 bald 正面相撞 |
| 配置 | Source 四层抽象、静默默认 | bconf 契约校验、J7/J9 fail-fast | 两套配置哲学不能共存于一个仓 |

**结论：按「移植清单制」四档处理，只搬 bald 的真空白。**

### 6.1 决策表

| 件 | onexmesh 证据 | 处置 | 方式 | 代价（人日） | 风险 |
|---|---|---|---|---|---|
| registry/cache 发现缓存层 | `pkg/registry/cache`（222 行，singleflight+双 TTL+stale-while-error+限流+注入时钟） | **移植** | 接口适配（onexmesh `Discovery` → bald `registry.Discovery` 形状对齐），作为 `registry/cache` 子包 | 1–2 | 低——纯客户端缓存，无后端耦合 |
| selector（round_robin/random/weighted/p2c+EWMA） | `pkg/selector`（p2c 含成功率 throttle） | **移植** | 新 module `selector`；Discovery 依赖适配；EWMA/cursor 状态随实例缓存 | 2–3 | 低 |
| gRPC balancer 适配 | `pkg/client/balancer`（selector→picker，策略走 address Attributes，实例按 (strategy,service) 缓存） | **移植** | 跟 selector 同 module 或独立；用 bald 当前 grpc 版本（无钉版债） | 1–2 | 低——balancer API 稳定 |
| resilience 的 hedge（对冲） | `pkg/resilience/hedge.go`（backup request） | **移植** | 进现有 retry module（正交维度，小件） | 0.5–1 | 低 |
| resilience 的 bulkhead（舱壁）+ shedder（降载） | `bulkhead.go`（core/limit 信号量）、`shedder.go`（CPU+利特尔法则） | **评估后移植** | bald ratelimit 的 bbr/sentinel 已含入站并发控制；舱壁是**出站调用方隔离**，角色不同——作为 retry/circuitbreaker 同级小件收编；shedder 与 bbr 重叠面需先对比 | 1–3 | 中——与 ratelimit 的语义边界要先写清楚 |
| otelslog slog→OTel log 桥 + TraceIDHandler | `pkg/otelslog`（642 行） | **行为对齐**（不抄代码） | bald log/bslog/otel.go 已有桥，对照它的 record 转换完整度补缺 | 1 | 低 |
| semconv 集中管理 | `pkg/middleware/semconv.go` | **组织性重构** | bald 的 semconv 散在 metrics/web 中间件——收拢成单文件（observability-r09 的落地动作） | 0.5 | 低 |
| 声明式 resiliency YAML（Dapr 风格） | `pkg/resiliency`（策略模板+端点绑定+状态码 matching） | **owner 裁决** | 若要进 bald，应作为 appkit mech 的一个机制（显式注册），不是独立子系统——设计调和 3–5 人日 | 3–5 | 中——与 mech 哲学需调和 |
| client SDK 面（Dial+resolver+clientset/fake/informers 生成器） | `pkg/client`（1.8k 行）+ `protoc-gen-onexmesh-client`（13 个 generator） | **owner 裁决**（bald 无客户端 SDK 产品面） | 若立项是新产品面不是移植；IDL 契约与 bald.store.v1 不通，需重新设计 | ≥10 | 高——产品级决策 |
| transport/middleware/server 三件套 | 核心桥 | **不移植** | 与 bald transport 17 子包正面冲突，哲学不兼容 | — | — |
| config 四层 | `pkg/config` | **不移植** | 与 bconf/bconfig/bootstrap 正面冲突，bald 立场更强 | — | — |
| errno/errorsx 复用层 | `pkg/errno` | **不移植**（设计可参考） | berrors 已覆盖同语义；Reason 双段命名习惯可借鉴进 berrors 文档 | — | — |
| registry 的 polaris/eureka 后端 | polaris（watch 最深）/eureka | **暂缓** | polaris 是 grpc 钉版债的根源；bald registry 现有 5 后端够用，加后端独立评估 | — | 中——grpc 版本债 |

### 6.2 每个移植包要付的「入会费」（bald 纪律的固定成本）

无论移植哪件，都叠加以下固定成本（已折算进上表的代价估计）：

1. **module 化**：独立 go.mod + tag + `release sync` 归位（多 module 发布流程一轮）；归属哪个 module 要过 `TestModulePackageSets` 登记。
2. **注册习语改写**：init 自注册 → `Type 常量 + contract/ Provider(ctx, cfg)`（broker-r07 模式），blank import 清零。
3. **错误/日志适配**：onexstack errorsx 构造点改 berrors 工厂；slog 调用改 log 契约层包级函数。
4. **设计文档**：docs/devel/zh-CN/ 一篇（bald 惯例：先有设计文档再有代码）；docscheck 链接闸门生效。
5. **测试迁移 + race**：自带 _test 迁移改断言目标；`task race`/`lint` 过闸；被 `-short` 跳过的用例登记 acceptance 表（testing-r05）。
6. **规范卡**：新能力面进 catalog 卡（一行指路条目）。

### 6.3 顺序建议

```
第一批（约 1 周，拿三大缺口）：registry/cache → selector+balancer → hedge
第二批（约 1 周）：bulkhead/shedder 对比收编 → semconv 收拢 → otelslog 行为对齐
第三批（裁决后）：resiliency 声明式进 mech；client SDK 面立项与否
不做：transport/middleware/server、config 四层、errno、polaris
```

### 6.4 什么情况下才值得整仓式吸收

只有一种：**放弃 bald 的声明式装配路线**，把 bald 降级为 onexmesh 的组件供应商（或反之，把 onexmesh 作为 bald 的默认运行时）。那是两个框架的合并，是产品战略决策不是工程决策——本文只记录：onexmesh 的 MeshServer 想进 bald 世界，接口上是 `transport.Server` 三方法（小适配）；bald 想进 onexmesh 世界，接口上是 `app.RunFunc`（onexstack 契约，bald 不依赖 onexstack，需桥接）。两个方向的适配面都不大，难的是决定了不回头。

## 附：证据与口径

- onexmesh 侧结论来自 2026-11-08 的 12 维度实盘清点（319 个 Go 文件、22.7k 行非测试代码、71 个 _test、8 处注册表习语逐一核对、go.mod 依赖清单、DESIGN.md 3.7 自述的 IDL 留白）；bald 侧结论来自本仓 docs/architecture/ 三份制品与本轮 22 张规范卡。
- 两处 bald 事实经当场核实：无 registry/cache 缓存层、无 selector/p2c（grep 全仓确认）；对冲（hedge）同样缺位。
- 局限：onexmesh 无 CI 运行记录可查（无 .github/），「远端是否绿」不可知；测试质量只按存在性盘点，未逐个评审断言强度。
