# 会话成果记录：onexmesh 吸收 × 方案 S 统一中间件契约（2026-11-08）

> 本文原为 2026-11-08 会话的交接记录。**交接已闭环**（2026-11-10）：§2 网络阻塞解除、
> §3 未完成清单全部落地、闸门全绿、发版队列清零——时效性章节已移除，保留两部分长期
> 有价值的内容：§1 成果总览（历史索引）与 §2 避坑清单（可复用知识）。

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

## 2. 避坑清单（长期有效）

### 2.1 网络与工具链

- `proxy.golang.org` 恒超时；**`goproxy.cn` 可用**（actionlint/bconf/新 tag 均经此装）。
- `github.com:443` 时通时断——push 失败先重试一次再换思路。
- 同仓新 tag 拉取：`GOPRIVATE='github.com/kalandramo/*' GOPROXY=direct GOSUMDB=off go mod download <mod>`。
- **buf pin**：`bconf/buf.version` = 1.57.2。跑 bconf 闸门确保 PATH 上是这把（`go test ./...` 的版本探针读 PATH）。

### 2.2 契约与生成

- **bconf 演进纪律**：新增字段纯增量、删除字段 `reserved` 不复用（bconf-r02）；
  改 proto 后 `task proto-contract`（= `cd bconf && buf generate --template buf.gen.local.yaml`）。
- **gen 布局**：bootstrap 类型在 `gen/go/bootstrap/v1`（无 bald 段）；appspec/store
  在 `gen/go/bald/{appspec,store}/v1`（有 bald 段）——**import 路径别写混**。
- cli 零 replace：同仓新 tag 发布后 cli 才能 require（release-r09：bconf 与 cli
  同批时 cli 排下一轮）。

### 2.3 运行时陷阱（实测）

- **gin 的 `c.Next()` 是循环**：middleware 返回后（即使未调 c.Next()）gin 推进
  索引继续跑链上剩余的 handler。统一桥短路必须显式 `c.Abort()`——bridge_gin.go
  错误路径已带，新桥同款必须带。
- **grpc v1.83 的 `balancer.SubConn` 外部不可实现**（internal.EnforceSubConnEmbedding）——
  balancer 单测只能测注册语义/回退，端到端走集成。
- **p2c 降权是概率性的**（采样到两个健康节点才提前收敛）——硬排除是
  selector/circuitbreaker 的职责（已入库 `selector/circuitbreaker/v0.1.0`）。
- **tokenbucket**：构造 `last: clock()` 已初始化，首个请求消费 burst 令牌——
  「首个请求即拒」的断言要用恒拒 limiter，别用 tokenbucket。
- **oneof 包装器字段名 = 策略消息名**（`Rule_RateLimit` 的字段叫 `RateLimit`）——
  govet composite 键检查要求具名初始化。
- **berrors 的 Code 是 google.rpc 码域**（不是 HTTP 状态码）——HTTP 状态经
  `httperr.HTTPToCode/CodeToHTTP` 映射（unified 桥的享元哨兵即此实现）。

### 2.4 测试断言口径

- 经桥的错误是 gRPC status（桥的 grpcerr 收口）——断言用 `status.Code(err)`；
  直调统一链才持有 berrors——断言用 `berrors.FromError`。
- p2c 降权是概率性的——断言用宽界（≥70%），别写 100%。
- EWMA 测试时钟陷阱：decayTime=10s，微秒级测试时钟下 w≈1 计数不动——
  置状态测选择行为，别模拟连续失败。
