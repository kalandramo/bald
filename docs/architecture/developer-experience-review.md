# bald 开发者体验体检（用户视角）

> 日期：2026-11-11。方法：沿「新用户 → 业务开发者 → 框架维护者 → AI 会话」四类真实用户
> 的旅程逐步走查，实证采集 CLI 真实输出与报错（标注【实证】的条目都有当次运行的原始输出
> 或一手踩坑记录佐证）；与 [risk-review.md](risk-review.md) 不重叠——那里记框架内部风险，
> 本文只记**使用者感知**的摩擦。每条给优化方向（一句话级，不展开设计），严重度：
> **P0**＝第一小时必卡/造成坏产物；**P1**＝高频摩擦或长期演进债；**P2**＝边缘改进。

---

## 1. 首次接触与安装

### DX-1 预设生成树缺 go.mod，next 提示不指路【实证】【P0】

`bald gen app --preset webserver` 的收尾提示是：
`next: edit meta.name/module (and addrs) in appspec.json, then run 'bald gen app --spec appspec.json'`——
用户照做后 gen 成功，紧接着 `go build ./...` 得到
`pattern ./...: directory prefix . does not contain main module or its selected dependencies`，
**没有任何出口**。生成的树引用 `pkg/appkit`、`bconf`、`transport/...` 等十余个 module，
go.mod 要手写且 replace 集要自己拼（本仓维护者实操时都拼错过一轮）。

**方向**：gen app 落树时检测无 go.mod 即生成一份可用基线（require 按 cli 编译时依赖快照 +
`replace` 按官方发布口径），或最低限度 next 提示给出可复制的 go.mod 模板 / `bald gen modfile`
子命令。quickstart 文档同步补「生成后的第一步」。

### DX-2 bald 二进制新鲜度不可见【实证】【P1】

真实 GOPATH 自定义时（本机即 Workspace/go 而非 ~/go），`go install` 落点与 PATH 命中的
二进制脱节——两个 bald 并存、PATH 命中旧版，行为与文档对不上且无任何警告（维护者实操
装错过两次，一次导致 gen 用了旧模板）。

**方向**：`bald version` 打印构建信息之外，`bald doctor` 增加 self 检查项：二进制路径、
构建时间、与远端最新 tag 的差距提示。

### DX-3 弱网环境的安装路径依赖代理索引【P2】

README 已给源码检出备选（好）；但「代理缓存未暖 → latest 拉不到」没有版本钉板的组合
建议（install @vX.Y.Z + 模块代理配置示例）。文档级修补即可。

---

## 2. 声明式装配的心智负担

### DX-4 appspec 可写什么无从发现【P1】

`bootstrap.server` 有 33 个传输面、`broker` 有 12 个后端、`middleware` 段八件——JSON 里
合法键的发现方式只有翻 proto 或抄 `_example`。手写 spec 撞 Gate1（报错质量不错，【实证】
`meta.module is required (generated files import <module>/internal/...)` 带原因），但
Gate1 只拦 meta 级必填；**嵌套段写错要到 Gate2/gen 才报**，且报错不列「该层可用的键」。

**方向**：由 protodesc 自动派生 appspec 的 JSON Schema 随 CLI 分发（IDE 补全 + 编辑期校验）；
Gate1/2 报错附「此层级可用的键」清单。

### DX-5 角色投影的「开关裁剪」语义静默【P1】

多角色下全局 `server.*` 段默认被 projectServer 裁掉，需 RoleSpec 开关（cron/asynq/mcp）
才投影进角色 YAML。语义在 proto 注释与卡里有解释，但用户撞到的形态是「配置了没生效」
——**静默**（有开关的面报错都兜不住，没开关的面在旧版本就是静默不装配）。

**方向**：gen app 对「全局声明了 server.X 段但无任何角色开关」给 warning；doctor 的
segment coverage 报告把「声明但未投影」单独列节。

### DX-6 概念密度高但报错无文档出口【P2】

Gate3「未知契约键启动即报」、L3 段覆盖、owned/seeded/user 三态、J1–J9——概念本身在
docs 与卡里有定义，但**运行期报错文案不链接出口**（新用户搜不到名词对应文档）。

**方向**：报错文案尾部统一挂 docs 锚点（如 `see: docs/devel/zh-CN/…#l3`）——一次性的
文案批量修订。

---

## 3. 日常开发循环

### DX-7 gen app 不发现 cwd 里的 appspec【实证】【P1】

`cd <app> && bald gen app` 报 `--spec <AppSpec.json> is required`——cwd 明明有
appspec.json。高频操作每次都要带参数（维护者实操撞了两次）。

**方向**：cwd 自动发现（无歧义时）+ 有歧义时列出候选；最低限度报错改写为
`--spec is required (found appspec.json in cwd — pass --spec appspec.json)`。

### DX-8 owned 文件误改后的恢复路径不指路【P2】

手改 `assemble.gen.go` 后 `--check` 红并给出 codegen-r03 finding（rule id ✓ suggestion ✓），
但「哪些文件是 owned、怎么还原」要自己想 manifest 清单。

**方向**：finding 的 suggestion 追加一行 `owned files are listed in appspec.manifest.json; restore via git checkout`。

### DX-9 「改了 X 要跑哪些门禁」无速查【P2】

AGENTS.md 的硬门禁表按**闸门**组织，不按**改动面**组织——改 `pkg/middleware` 与改
`docs/` 与改 proto 的最小门禁集不同，贡献者每次要自己对表。

**方向**：AGENTS.md 或 convention 卡加一张「改动面 → 最小门禁集」速查表（约 10 行）。

---

## 4. 依赖与本地调试

### DX-10 业务项目本地调试框架缺成文工作流【P1】

业务项目想本地改 bald 源码验证（改框架 bug / 试未发版本）：go.work 是解法，但
**go.work 存在时跑 `go mod tidy` 会把 workspace 成员物化成 go.mod replace**——零 replace
纪律的项目就这样被污染（维护者实操踩过，污染 25 行 replace）。

**方向**：成文「本地调试三步」（go.work → 测试用 `go test -mod=mod` → 收尾删
go.work 再 tidy），或给 `bald doctor` 加 go.work 残留检测；tidy 物化行为在文档里显式警告。

### DX-11 版本配套矩阵缺失【P1】

bald 根 / pkg / bconf / cli / transport 系各自发版；业务升 bald 时「哪个 cli 配哪个
bconf/pkg」无 compatibility matrix，生成树的 require 版本由 cli 编译时快照决定——
升级后生成树与运行时库版本漂移、无提示。

**方向**：release changelog 生成时产出配套表（cli vX ↔ bconf Y ↔ pkg Z，tag 同批即
同组）；gen app 在生成树 require 与本机 `bald version` 不配套时提示。

---

## 5. 出错时刻

### DX-12 字节闸门的工具链幽灵【P1（贡献者视角）】

`TestGeneratedMatchesProto` 的报错文案本身很好（三轴诊断 + 复跑指令），但存在连文案都
救不了的形态：**本机 go build 缓存产出过错口味 protoc-gen-go（中文注释空格），闸门偶发
红、多跑即绿**——排查要懂 pin/口味/缓存三层，新贡献者无法自助。

**方向**：闸门测试把「连续两次结果不一致」识别为缓存口味问题并直接给出
`go clean -cache` 指令；文档记录该幽灵。

### DX-13 L3 启动报错指路不到 contract 包【实证输出好、出口缺】【P1】

`unconsumed segments fail startup (L3); provide a Provider or whitelist explicitly`——
诊断到位，但「provide a Provider」对新手是黑话：该段对应的 contract 包在哪？

**方向**：报错按段名附建议（broker.redis → `broker/redis/contract.AppProvider()`、
database.sql → contrib 对应包——框架自有的段可静态映射，一张小表）。

---

## 6. 发版与维护（框架维护者）

### DX-14 release sync 全树归位不可裁剪，裁剪即坏 tag【实证】【P0（维护者）】

裁剪 release-plan 只留 2 模块时，sync 仍把**全树** go.mod 归位到计划内 next 版本——
cli/v0.7.0 因此 require 了未发的 cobramcp v0.3.5 成坏 tag（靠 v0.7.1 收口）。
plan 的不变量是「整批发版」，但工具不阻止你裁剪。

**方向**：sync 增加「归位目标版本 tag 必须已存在（或同批即将打）」的守卫，违者拒绝；
或 plan 支持 `--only <module>`（内部保证闭包内归位）。**坏 tag 的补救流程**（删远端 vs
patch 版收口）成文。

### DX-15 release tag 的 plan-head 守卫拒绝后无出路【实证】【P2】

HEAD 比计划多非发布改动时 tag 拒绝并提示「重新运行 plan」——但重跑会重算全清单（与
DX-14 连锁），实际解法（手工 tag，有 §7.40 先例）不成文。

**方向**：报错补一行「增量提交后重跑 plan，或按 §7.40 先例手工 tag」。

---

## 7. AI 会话视角（AI 化定位）

### DX-16 gen 的只读面不在 MCP【P1】

convention 四命令已进 MCP ✓，但 `gen app --check`（AI 验证声明一致性的高频动作）与
`doctor` 不在工具面——AI 想验证只能让用户跑。selector 设计是防 destructive（gen --adopt
会写树），但 **check/doctor 是只读的**，挡在外面是过严。

**方向**：`gen app --check`（只读）与 `doctor`（只读）进 cobramcp 工具面，adopt 仍挡。

### DX-17 诊断输出无 JSON 形态【P2】

doctor / gen --check 的产出是给人看的文本；AI 消费要靠解析 markdown 表格。
convention check 已有 --json ✓，其余诊断面未跟上。

**方向**：doctor 与 gen --check 加 `--json`（复用 cobramcp 的 structuredContent 通道）。

---

## 8. 快赢清单（按性价比排序）

| # | 项 | 代价 | 对应 |
|---|---|---|---|
| 1 | preset 生成树落 go.mod 基线（或 next 提示给模板） | 0.5–1 人日 | DX-1 |
| 2 | gen app cwd 发现 + 报错改写 | 0.5 人日 | DX-7 |
| 3 | L3 报错附 contract 包建议表 | 0.5 人日 | DX-13 |
| 4 | gen --check / doctor 进 MCP（只读） | 0.5 人日 | DX-16 |
| 5 | 报错文案挂 docs 锚点（批量修订） | 1 人日 | DX-6 |
| 6 | release sync 归位守卫（坏 tag 防复发） | 1–2 人日 | DX-14 |
| 7 | appspec JSON Schema 派生 | 2–3 人日 | DX-4 |

## 附：与既有制品的关系

- [risk-review.md](risk-review.md)：框架内部风险与消解条件——本文是其**用户感知面**的补充，不重复登记。
- [onexai-gap-review.md](onexai-gap-review.md) §4：AI 化缺口清单——本文 DX-16/17 与其 P1-7
  （review 工作流）相邻，落地时合并排期。
- 本文的实证输出（preset next 提示、Gate1 报错、`go build` 崩溃、`--spec is required`）
  采集于 2026-11-10/11，`bald` @ `cli/v0.7.2` 行为；后续修复时以本文条目勾销。

---

## 9. 文档体系体检（DX-18…28，2026-11-11 增补）

> 上述 17 条查的是**框架**对用户不友好的地方；本节把**文档本身**也当用户体验对象体检。
> 口径：四层文档实盘清点（guide 17 篇/4.7k 行、devel 43 篇/11k 行、architecture 10 篇/3.9k 行、
> 蒸馏卡 22 张）+ 过时信号扫描（改名 API/旧路径/计数漂移的 grep 实证）+ quickstart 首篇
> 逐段跟随。DX-27/28 与 risk-review 的文档类风险相邻，落地时合并。

### 结构性

**DX-18 修订日志侵占导航位【实证】【P2】**
`docs/architecture/README.md` 是架构制品的**索引入口**，但一半篇幅是 2026-11-02 五次
"重核"修订注记（每次 4–8 行）+ 2026-11-06/10/11 增量——变更史有价值，但它把
「哪份文档回答什么问题」的导航表挤到中间，新读者要先翻过 40 行历史才看到索引。

**方向**：索引与修订史分离——README 只留索引表 + 校验命令；修订注记收敛到文内
「修订记录」小节或 architecture 层的 CHANGELOG。

**DX-19 计数型断言无守卫，漂移即过时【实证】【P1】**
`llms.txt` 写"开发手册 32 篇"——磁盘实际 **43 篇**（11 天内 +11 篇没人回头改）。
同类：README 的 contract 包计数 30→39（靠人工五次重核追平）、卡数 16→22。
凡是「N 篇/N 张/N 个」的句子都是未来的过时内容。

**方向**：docscheck 增加计数守卫（llms.txt/README 的篇数断言与 `find` 实数一致）；
或者文档层面立规：**索引句不写具体计数**（"quickstart 起的使用文档集合"），数字只
出现在可被闸门校验的地方。

**DX-20 devel 层的导航好，architecture 层缺同款【实证】【P2】**
`docs/devel/zh-CN/README.md` 有「你关心的事 → 先读哪篇」的分流表（好实践）；
`docs/guide/zh-CN/README.md` 同款 ✓；architecture 层没有——它的 README 是制品校验
说明（见 DX-18），"新维护者按问题找文档"的入口缺失。

**方向**：architecture README 头部加同款分流表（~8 行：找风险→risk-review、找当前
结构→current-architecture、找 AI 化结论→onexai-gap-review…）。

### 可读性

**DX-21 quickstart 是维护者密度，不是新用户密度【实证】【P1】**
《用 bald CLI 起步新服务》第 2 节（安装 CLI）用三段 200–400 字的段落把**核心命令**
（`go install` 两行）埋进：module 拆分理由、三个历史 tag 的报错原文、replace 计数取证、
代理滞后的路径遮蔽案例、tools.go 钉版本的完整惯例（含 tidy 行为机理）、sum.golang.org
延迟……每条考证都是真金白银的知识，但**新用户在"30 秒最小路径"之后进来的第一篇
文章就劝退**。字段参考表（§3.1）反而是全文最好读的部分。

**方向**：正文只留可跟随的命令流 + 每步一句「这步在做什么」；考证/历史/避坑下沉到
「背景与坑」折叠节（`<details>` 或文末附录）——内容不减，密度分层。

**DX-22 术语在 guide 层裸用，定义在两层之外【P2】**
Gate1/Gate2/Gate3、owned/seeded/user、L3 段覆盖、J1–J9——guide 行文直接使用
（如「owned 的 `Options()`」「Gate1 校验」），定义散在 devel 设计文档与《设计判据》。
guide 读者（业务开发者）没有这些前置。

**方向**：guide 首篇或 README 加一张 10 行术语速查表（名词 + 一句定义 + 定义所在文档）；
或报错/文档首次出现处统一挂锚点（与 DX-6 合并做）。

### 实用性

**DX-23 guide 教的是 deprecated 面，统一链零指南【实证】【P0（文档口径）】**
方案 S 批次 1–4 落地后：`pkg/middleware` 根的 Handler/Transporter/matcher/三座桥、
`server.unified_middleware` 契约段、bundle 的 `WithUnifiedMiddleware`——**能力地图已补**
（框架契约总览 §9.3），但 guide 层**零覆盖**：现有《Bald 中间件设计》与 quickstart 教的
`bundle.FromMiddleware`/`newGRPCChain` 全是 deprecated 旧链。新应用按 guide 写，
写出来的就是批次 4 要移除的面。

**方向**：guide 新增《统一中间件契约》一篇（统一段怎么声明、九件内置件怎么配、
matcher 怎么选路由）；旧链相关篇目头部挂 deprecated 横幅并指向新篇。**这是文档侧
批次 4 的前置**（与 memory 的批次 4 推进顺序呼应：codegen 统一段生成 → guide 同步）。

**DX-24 CLI 全貌与 guide 覆盖面错位【实证】【P2】**
`bald --help` 有五个命令族（gen/convention/ide/release/doctor 随 gen），quickstart 的
安装节结尾写「CLI 提供 4 个生成子命令」——**gen 之外的三个族**（convention 查规范、
ide init 接 AI 工具、release 发版）在 quickstart 视野之外；「doctor 看装配诊断」这类
日常高频动作没有 quickstart 级介绍。

**方向**：quickstart 加一节「gen 之外」（每族三行：干什么 + 一条命令 + 卡/手册链接）。

**DX-25 手写 appspec 与预设的关系未讲【P2】**
quickstart 主线是手写 appspec 全字段；`gen app --preset`（现七预设）是更优起点但
两者在 guide 里无衔接——什么时候预设、什么时候手写、预设生成后怎么演进，读者要
自己悟（DX-1 的 go.mod 坑又叠在这一步上）。

**方向**：《用 bald CLI 起步新服务》重排为「预设起步 → 手写字段参考 → 预设到手写的
演进」三段式。

### 过时内容剔除

**DX-26 逐字存档类的"活性"边界要显式【P2】**
`onex-ai.md`（逐字存档）口径是"保持原样不编辑" ✓；但同类性质的过时内容散在活性文档里：
「框架不会自己读段」这句旧口径在契约总览里是唯一带"已作废"标记的——**其它文档的
作废内容没有标记机制**（grep "已作废/旧口径" 全 devel 仅 1 处命中），读者无从分辨
哪些句还是现行事实。

**方向**：立一个轻量口径——被替代的段落不删，改写为 `> ⚠️ 已作废（<日期>）：<现行口径>`，
让"过时"变成显式信号而不是读者自行考古。

**DX-27 能力地图与代码同步靠人工追【实证】【P1】**
框架契约总览 §9.3 缺统一链面（本会话补上）是实例：能力地图（675 行手写表）的刷新
依赖"有人想起"，发版流程不含它。同类风险面：模块清单、contract 包计数、卡规则数。

**方向**：release 四步的 sync 环节加一项 checklist（能力地图增量节、llms.txt 篇数、
README 计数），或对契约总览做「符号存在性」的 docscheck 抽查（表中 `pkg/x.Foo` 必须
真实存在——check_cites 的同款思路延伸到 API 引用）。

**DX-28 过时扫描的实证记录（本次清扫基线）【P2】**
本轮 grep 实证：`observability/metricshttp` 旧路径在 docs/**零残留**（指标设计文档已是
`pkg/metricshttp` 新路径 ✓——五次重核的成果）；`FromMiddleware` 在 guide 的用法描述
**与代码现状一致**（旧链未删、描述不算错，但按 DX-23 挂 deprecated）；计数漂移仅
DX-19 一处实证。「已作废」标记仅 1 处（DX-26）。

**方向**：本节作为文档体检的**基线存档**——下次体检对照增量；grep 模式
（旧路径/作废标记/计数断言）沉淀进 docscheck 或 tools/ 校验器，让"过时扫描"可复跑。

### 文档体检快赢补充（接 §8 表）

| # | 项 | 代价 | 对应 |
|---|---|---|---|
| 8 | guide《统一中间件契约》新篇 + 旧链篇挂 deprecated 横幅 | 1–2 人日 | DX-23 |
| 9 | llms.txt/README 计数守卫进 docscheck（或立"索引不写计数"规） | 0.5 人日 | DX-19 |
| 10 | quickstart 密度分层（正文命令流 + 背景折叠） | 1 人日 | DX-21 |
| 11 | guide 术语速查表（10 行） | 0.5 人日 | DX-22 |
| 12 | architecture README 索引/修订史分离 + 分流表 | 0.5 人日 | DX-18/20 |
