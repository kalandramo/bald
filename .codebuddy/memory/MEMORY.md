# MEMORY（bald 仓库内同步副本）

> 本文件为跨设备同步副本，源在 workspace 根 `.codebuddy/memory/MEMORY.md`。
> 精简保留长期稳定事实；每日明细见 `YYYY-MM-DD.md`。

## 工作区结构
根 `konglingfei/` 是多个**独立 Go module** 集合（非单仓库）：`bald`、`go-lulu`、`go-wind-toolkit`、`cobrax`、`easyai`、`go-utils`、`kratos`、`miniblog`、`onex`、`onexstack`、`osbuilder`、`protoc-gen-defaults`。`bald` 是其中独立 git 仓库，路径 `konglingfei/bald/`。

## bald 核心架构共识（P0–P9）
文档 `bald/docs/devel/zh-CN/架构演进路线.md`。原则：
1. **Proto 单一真相源**（配置/API/类型全 proto 生成）。
2. **核心零后端耦合**——引擎/桥接走独立 go.mod（P5）。
3. 函数式 Option DI，不用 wire/fx/dig。
4. 依赖倒置中间件/拦截器，核心不钉 protovalidate/otel。
状态（2026-08-30）：**P0-P4 已落地**；P7 认证授权、P8 多租户、P9 数据权限代码已落地（见下「近期决策」）。P2.3 扩列引擎暂不新建。

## 配置管理（pkg/options 已废弃）
- `pkg/config` viper 四源（flag>env>文件>远程），**勿替换**。
- `pkg/conf` 框架配置用 proto 生成（`api/proto/bald/config/v1/`），提供 `NewBootstrap/Validate/BindFlags/ResolveTLS/Scheme/LogOptions`。
- server 直接消费 `confv1.Http/Grpc` 指针；flag 经 `appkit.Bind(prefix,opt)` 注册。配置键四源一致：`--http.addr` ⇔ `http.addr` ⇔ `BALD_DEMO_HTTP_ADDR` ⇔ 文件 `http.addr`。

## 桥接子模块（独立 go.mod，在 bald/contrib/）
- `bald/contrib/store-gorm`：GORM 实现 `store.Store[T]`（替 ent）。`replace => ../..`。
- `bald/contrib/authn-jwt`：实现 `authn.Authenticator`（HS256 白名单，无硬编码密钥）。
- MVP 如需 Redis/Task，新增 `bald/contrib/store-redis`、`bald/contrib/task`。

## 认证/授权/租户/数据权限（P7/P8/P9）
- `pkg/authn`：`Authenticator` 接口 + `AuthClaims`（Subject/TenantID/Name/Scopes/Roles/ExpiresAt/Issuer）+ context 注入。**实现必须校验 ExpiresAt 过期**（中间件不再重复判断）。
- `pkg/authz`：`Authorizer` 接口（Func/AllowAll/DenyAll）；不引入 casbin 入核。
- `pkg/store/tenant.go`：`RegisterTenant(key, fn)` + `Where.T(ctx)`；`tenant_id` **需显式注册** `DefaultTenantFunc` 才开启（不在 init 隐式注册）。`mergeTenant` 去重覆盖任意 Op。
- `pkg/store/scope.go`：`RegisterDataScope(fn(ctx,*AuthClaims)[]*FilterCondition)`；`mergeDataScope` 过滤 nil/空字段条件。
- gin/grpc 中间件：`middleware/{gin,grpc}` 用 `Authenticator`/`Authorizer`，Bearer→`ContextWithToken`→`Authenticate`→`ContextWithAuthClaims`；gin 失败打 `log.Error`。

## 关键坑（长期有效）
- `config.Load` 的 `BindPFlags` 仅对 `flags.Changed` 的 flag 调 `BindPFlag`（防零值压过 env/文件）。
- `GRPCServer`/`GatewayServer` 必须实时读 `s.cfg.GetAddr()`，不可缓存构造期快照；Gateway 延迟到 `Start` 建 conn 且 attach 完立即释放 `mu` 防死锁；后端地址不能用 `:0`。
- handler 返回 `*berrors.Error` 须在最外层挂 `grpcmw.ErrorInterceptor()`（调 `grpcerr.ToStatus`）否则被兜底成 Unknown。
- protojson 替换语义 + `proto.Merge` repeated 是 append（合并前 Clear）；Duration 用 `FormatFloat(d.Seconds(),'f',-1,64)+"s"`。
- `_example` 被 `go build/test ./...` 忽略；验证用 `go vet ./_example/bald/` 或 `task verify`。
- `gofmt -l` 未过的既有文件改造时只格式化自己改动的文件，避免无关 diff。

## 近期决策索引（明细见 daily）
- 2026-08-29：配置 proto 化、server 地址同步修复、P6 校验分层、gRPC 错误透传接线、Makefile→Taskfile。
- 2026-08-30：P7/P8/P9 实现 + CR 6 项修复；contrib 子模块从仓库外搬入 `bald/contrib/`；规划 `bald/examples/go-bald-admin`（用 bald 重构 go-wind-admin/backend 的官方范例，选型见该目录 docs/）。

## 用户偏好
- 渐进式改造，要求指明代价与风险；实现后记录文档。
- 验证类代码（e2e/冒烟）保留入仓库并接 Taskfile，勿一次性删。
- 决策风格：给评估后说「需要/直接」即采纳，期待直接落地。
- 跨设备开发：记忆需同步进 bald 仓库（`bald/.codebuddy/memory/`）以便 git 拉取。

## 环境/工具
- protoc 35.1、buf 1.57.2、go 1.26.5、task 3.53.1。
- macOS 无 `timeout`；`golang-jwt/v5` ParseWithClaims 默认校验 exp。
