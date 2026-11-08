# 工作交接：onexmesh 吸收 × 方案 S 统一中间件契约（2026-11-08）

> 本文档是 2026-11-08 会话的交接记录：当日完成 onexai 差距评审落地、onexmesh 吸收
> 前两批、方案 S 统一中间件契约的批次 1–2 与批次 3 装配面，累计九个 tag 发布。
> 后续会话从「当前阻塞」一节接续。

---

## 1. 会话成果总览

### 1.1 评审与设计（全部入库 docs/architecture/）

| 文档 | 内容 |
|---|---|
| `onexai-gap-review.md` | onexai 六层依赖清单 × bald 对照；方案 S（统一中间件契约）设计与四批迁移路径；完成度标注 |
| `onex-ai.md` | onexai 工具输出逐字存档（46 convention / overview / 20 create / PRD 样例 / 第 13 章节选） |
| `onexmesh-comparison.md` | onexmesh（运行时框架）12 维度盘点 × bald 对照 + 移植评估决策表（§6 完成度已回填） |
| `transport-unification-assessment.md` | 传输统一抽象重构评估：痛点拆解、S/M/L 三档、四批迁移路径 |

### 1.2 代码交付（全部推送 origin/main）

| 批次 | 内容 | tag |
|---|---|---|
| 方案 S 批次 1 | Transporter（协议无关元数据载体）+ Handler/Middleware/Chain 契约 + 三座服务端桥 + matcher | `pkg/v0.7.0` |
| 方案 S 批次 2 | 九件统一内置件（Recovery/RequestID/Timeout/Authn/Authz/Audit/RateLimit/Validator/Defaulter）+ 桥内置 grpcerr 收口 + authz.AuthorizerFunc | `pkg/v0.8.0` `pkg/v0.9.0` |
| 方案 S 批次 3 装配面 | bconf 契约 `server.unified_middleware` 段（v0.24.0）+ Bundle 适配器（WithUnifiedMiddleware/WithUnifiedValidator → matcher → 三座链头预置）+ **gin 桥 Abort 修复** | `bconf/v0.24.0` `pkg/v0.10.0` |
| onexmesh 吸收第一批 | registry/cache（发现缓存）+ selector（四策略）+ selector/balancer（gRPC 适配）| `registry/v0.5.0` `selector/v0.1.0` `selector/balancer/v0.1.0` |
| onexmesh 吸收第二批 | retry/Hedger（对冲）+ retry/Bulkhead（出站舱壁）；shedder 不移植（与 bbr 同物） | `retry/v0.3.0` |

### 1.3 发现并修掉的上游 bug

1. **onexmesh 池化 bug**：`ReleaseTransporter` 必须复位 grpcCode——上游注释称零值无需复位是错的（`SetGRPCCode(Internal)` 后复用实例带 Internal 进下一请求）。bald 版复位，测试钉住。
2. **bconf pin 漂移**：`buf.version` 写 1.50.0 但入库树实为 1.57.2 所产——版本探针被漂移遮蔽，内容对比从未真正跑到。已修 pin 至 1.57.2。

---

## 2. 当前阻塞（接手第一件事）

**`cli/internal/codegen` 的 e2e 测试红**：临时测试模块的 `go mod tidy` 拉不到
`retry@v0.3.0` / `bconf@v0.24.0` / `pkg@v0.10.0` / `registry@v0.5.0` 的 zip——
这四个 tag 刚推送，代理缓存未暖，且本沙箱 `proxy.golang.org` 超时、
`github.com:443` 时通时断。

**解法（网络窗口内执行一次即可）**：

```bash
cd cli
for m in "github.com/kalandramo/bald/retry@v0.3.0" \
         "github.com/kalandramo/bald/bconf@v0.24.0" \
         "github.com/kalandramo/bald/pkg@v0.10.0" \
         "github.com/kalandramo/bald/registry@v0.5.0"; do
  GOPROXY=https://goproxy.cn,direct go mod download "$m"
done
GOPROXY=https://goproxy.cn,direct go test ./internal/codegen/ -count=1
```

- `goproxy.cn` 可用（本会话经它装了 actionlint、拉了 bconf）；`proxy.golang.org` 恒超时。
- 预热后 e2e 恢复绿；CI（远端）不受本沙箱网络影响，应自行通过。
- **这不是代码问题**：阻塞前全量闸门（`-race` + golangci 0 issues + docscheck）全绿。

---

## 3. 未完成清单（按优先序）

### 3.1 方案 S 收尾

1. **批次 3 剩余**：`web.ErrorResponse` 委托 `pkg/middleware` 渲染原语（J2 清偿；
   桥内渲染与 web 同形已实现，收口=web 改为调 middleware 的导出渲染函数）；
   旧链 `Gin()/GRPCInterceptors()` 标记 deprecated（批次 4 移除）。
2. **批次 2 继续**：authn/authz/audit/observability 等重协议耦合件的两链旧实现
   逐个改写为统一形态（九件统一内置件已是可用实现，旧链保留至批次 3 收口）。

### 3.2 onexmesh 吸收剩余

3. **semconv 收拢**（1–2 人日）：bald 的 semconv 散在 metrics/web 中间件，参照
   onexmesh `pkg/middleware/semconv.go` 收拢成单文件（observability-r09 落地）。
4. **otelslog 行为对齐**（1–2 人日）：对照 onexmesh `pkg/otelslog`（642 行）补
   bald log/bslog OTel 桥的 record 转换完整度。
5. **WithCircuitBreaker**（1–2 人日）：selector 按节点熔断——需先给 bald
   circuitbreaker 契约加 Healthy 查询语义（或独立 BreakerRegistry）。
6. **resiliency 声明式 YAML 进 mech**（裁决后 3–5 人日）／**client SDK 面**
   （≥10 人日，产品决策）——owner 裁决项。

### 3.3 独立大项

7. **_example 补 job/mq/mcp 三示例**（需 e2e 设计：job=worker+cron 接线、
   mq=broker 段+消费者、mcp=transport/mcp）。
8. **mq/mcp/clitool 预设**：等 cron/mcp/出站段的契约形状核实后再加（预设不猜字段）。

### 3.4 用户侧

9. zcode 真机验证（业务项目 `bald ide init --ide=zcode` → Settings→MCP → 会话取卡）。
10. workbuddy 规则/MCP 文件格式（拿到后补 ide init 差异面）。
11. PRD 方法论课 PDF（Phase 3 做 `bald prd` 时取）。

---

## 4. 关键技术事实（避坑清单）

### 4.1 网络与工具链

- `proxy.golang.org` 恒超时；**`goproxy.cn` 可用**（actionlint/bconf/新 tag 均经此装）。
- `github.com:443` 时通时断——push 失败先重试一次再换思路。
- 同仓新 tag 拉取：`GOPRIVATE='github.com/kalandramo/*' GOPROXY=direct GOSUMDB=off go mod download <mod>`。
- **buf pin**：`bconf/buf.version` = 1.57.2（本会话修正）。跑 bconf 闸门确保 PATH 上
  是这把（`go test ./...` 的版本探针读 PATH）。

### 4.2 契约与生成

- **bconf 演进纪律**：新增字段纯增量、删除字段 `reserved` 不复用（bconf-r02）；
  改 proto 后 `task proto-contract`（= `cd bconf && buf generate --template buf.gen.local.yaml`）。
- **gen 布局**：bootstrap 类型在 `gen/go/bootstrap/v1`（无 bald 段）；appspec/store
  在 `gen/go/bald/{appspec,store}/v1`（有 bald 段）——**import 路径别写混**。
- cli 零 replace：同仓新 tag 发布后 cli 才能 require（release-r09：bconf 与 cli
  同批时 cli 排下一轮）。

### 4.3 运行时陷阱（本会话实测）

- **gin 的 `c.Next()` 是循环**：middleware 返回后（即使未调 c.Next()）gin 推进
  索引继续跑剩余 handler。统一桥短路必须显式 `c.Abort()`——已修（bridge_gin.go
  错误路径），新桥同款必须带。
- **grpc v1.83 的 `balancer.SubConn` 外部不可实现**（internal.EnforceSubConnEmbedding）——
  balancer 单测只能测注册语义/回退，端到端走集成。
- **p2c 降权是概率性的**（采样到两个健康节点才提前收敛）——硬排除是
  WithCircuitBreaker 的职责（未移植，需 Healthy 查询语义）。
- **tokenbucket**：构造 `last: clock()` 已初始化，首个请求消费 burst 令牌——
  「首个请求即拒」的断言要用恒拒 limiter，别用 tokenbucket。
- **oneof 包装器字段名 = 策略消息名**（`Rule_RateLimit` 的字段叫 `RateLimit`）——
  govet composite 键检查要求具名初始化。
- **berrors 的 Code 是 google.rpc 码域**（不是 HTTP 状态码）——HTTP 状态经
  `httperr.HTTPToCode/CodeToHTTP` 映射（unified 桥的享元哨兵即此实现）。

### 4.4 测试断言口径

- 经桥的错误是 gRPC status（桥的 grpcerr 收口）——断言用 `status.Code(err)`；
  直调统一链才持有 berrors——断言用 `berrors.FromError`。
- p2c 降权是概率性的——断言用宽界（≥70%），别写 100%。
- EWMA 测试时钟陷阱：decayTime=10s，微秒级测试时钟下 w≈1 计数不动——
  置状态测选择行为，别模拟连续失败。

---

## 5. 闸门现状（交接时点）

| 闸门 | 状态 |
|---|---|
| `-race`（pkg/middleware 全部 + bundle + codegen 单元面） | ✅ 绿 |
| golangci（pkg + cli + registry + retry + selector） | ✅ 0 issues |
| docscheck（含 module-list 登记 selector/selector/balancer） | ✅ 绿 |
| bconf 字节闸门（buf 1.57.2 pin 修正后） | ✅ 绿 |
| **cli/internal/codegen e2e** | ❌ **网络阻塞（§2）** |
| task ci-workflow-lint | ✅ 绿 |

---

## 6. 接手检查清单

1. `git pull` → 确认 HEAD ≥ `3ceb5db`（批次 3 装配面）+ 五个 tag。
2. 解 §2 网络阻塞（预热代理缓存）→ `go test ./internal/codegen/` 转绿。
3. 跑 `task lint && task race`（全量）确认本机基线。
4. 按 §3.1 做方案 S 收尾（web 委托 + deprecated 标记，一个提交）。
5. §3.2/§3.3 按优先序推进；每批独立提交 + `bald release` 四步发版。
6. 发版纪律：plan → sync → changelog → tag；**bconf 与 cli 同批时 cli 排下一轮**；
   tag 推送每批 ≤3 个（git push 分批）。
