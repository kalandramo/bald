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
