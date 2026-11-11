# Bald 统一中间件契约（server.unified_middleware 段）

> 本文是统一中间件契约（方案 S）的**使用指南**。旧链（`bundle.FromMiddleware` /
> `bundle.Gin()` / `bundle.GRPCInterceptors()`）已标 deprecated（保留至下一 major），
> 新装配一律走本篇的统一段。两套并存的现状与迁移理由见《Bald 中间件设计》头部说明。

## 1. 它解决什么

同一份中间件规则要写两遍（gin 链 + gRPC 拦截器链）、路由级差异化做不到（`server.{http,grpc}.middleware`
段是 server 级）——统一契约把这两个问题一次收掉：

- **一份签名**：`middleware.Handler func(ctx, req any) (any, error)`（与 `grpc.UnaryHandler`
  同形）+ `Middleware func(Handler) Handler`；九件内置件（Recovery/RequestID/Timeout/
  Authn/Authz/Audit/RateLimit/Validator/Defaulter）各写一遍，双协议共用。
- **路由级选择**：规则带 `selector`，按「方法 + 路由模板」或 gRPC FullMethod 命中。

## 2. 声明：契约段一行起

在 AppSpec 的 `bootstrap.server` 下声明 `unified_middleware`（多角色时配合 RoleSpec
开关投影进角色 YAML，语义同 cron/asynq）：

```yaml
server:
  unified_middleware:
    rules:
      # 全局限流：令牌桶 100 QPS（burst 200 吸收突发）。触顶 429，后续 handler 不执行。
      - selector: "*"
        rate_limit:
          rate: 100
          burst: 200
      # 路由级：只对管理写接口收紧（HTTP 面 selector = METHOD + 路由前缀）。
      - selector: "POST /v1/admin/*"
        rate_limit:
          rate: 20
          burst: 10
      # gRPC 面 selector = FullMethod。
      - selector: "/bald.v1.GreetService/Greet"
        timeout:
          defaultTimeoutMs: 3000
```

### 2.1 selector 语法（三层，`pkg/middleware/matcher`）

| selector | 命中 |
|---|---|
| `*` | 全部请求（双协议） |
| `POST /v1/admin/*` | HTTP：方法 + 路由前缀（路由模板聚合，不匹配原始 path） |
| `/pkg.Svc/Method` | gRPC：FullMethod |

### 2.2 策略面（oneof，五件）

`recovery` / `request_id` / `rate_limit` / `timeout` / `validate`。

⚠️ `validate` 需要装配代码注入校验回调（`bundle.WithUnifiedValidator`）——段声明了
validate 而未注入回调，**链构建期 fail-fast**（`UNIFIED_VALIDATOR_MISSING`）。不确定就
别声明：声明了却静默不生效是最坏结果（J7）。

## 3. 生效位置与短路语义

统一链经 matcher 在 **gin 与 gRPC 两面链头**生效——先于面段老链（`server.{http,grpc}.middleware`
翻出来的那条）。规则拒绝时：

- gin 面：按错误渲染状态码与 JSON 包络（与 `web.ErrorResponse` 同形），且 **Abort**——
  后续 handler 不执行（gin 的 `c.Next()` 是循环，桥内已处理）；
- gRPC 面：错误经 grpcerr 收口为 gRPC status（限流触顶 → `ResourceExhausted`）。

段缺省（不写 `unified_middleware`）零影响——opt-in 承诺。

## 4. 装配接线（bundle 一行）

```go
ginBundle := bundle.New(
    bundle.Audit(...), bundle.Metrics(...), bundle.Normalized(),
    // 契约段 → 统一链。规则非法在 Gin()/GRPCInterceptors() 构建期 fail-fast。
    bundle.WithUnifiedMiddleware(cfg.GetServer().GetUnifiedMiddleware()),
)
router.Use(ginBundle.Gin()...)
```

参考实现：bald-admin `cmd/bald-admin/app/assembly.go`（gin 面）。

## 5. 与旧链的关系

| | 统一链（本篇） | 旧链（deprecated） |
|---|---|---|
| 规则来源 | `server.unified_middleware` 段 | `server.{http,grpc}.middleware` 八段 |
| 生效位置 | 双面链头（先执行） | 面段（后执行） |
| 中间件实现 | 九件统一形态，写一遍 | gin/gRPC 各一份 |
| 移除计划 | —— | 下一 major（`pkg/middleware/shared` 已退场） |

两链并存期内：统一链先执行，规则短路后老链不跑；不冲突但要避免**同一职责两边都配**
（如统一段和面段都配 rate_limit——会双重限流，语义上是两道独立闸门）。

## 6. 验证

```bash
# 声明一致性（MCP 工具 bald_gen_app 的只读形态同款）
bald gen app --spec appspec.json --check
# 启动后 doctor 看装配计划里的统一链节点
<app> doctor
```

端到端判据：`pkg/middleware/bundle/unified_test.go`（双面短路 + validate fail-fast +
段缺省零影响）。
