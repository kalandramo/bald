# Bald 注册中心设计：契约零依赖、后端各自独立 module、装配只认显式注册

> Author(s): bald 团队
>
> Last updated: 2026-09-17
>
> Discussion at: 源码 `registry/`、`bootstrap/registrar.go`、`pkg/appkit/registrar_test.go`
>
> Status: Accepted（已实现且已发版；RegistrarRegistry 2026-09-17 迁入 bootstrap，见「实现与过渡」）

## 摘要

服务注册/发现被拆成三层：**契约**（`registry` module，四个类型，只 import `context`）、**后端**（`registry/<backend>`，一个后端一个独立 module，直连各自 SDK）、**装配**（`bootstrap` 的 `RegistrarRegistry`，显式 `MustRegister` + 按契约 `registry.type` 单选分发；appkit 只保留 `WithRegistrarRegistry` Option 与注册/反注册运行期编排）。**最重要的承诺：注册中心后端的依赖图里没有主模块，契约的依赖图里没有任何东西。** 契约就是 `registry.go` 一个文件 51 行，`go.sum` 是空的；四个后端各自 require `registry` + `bconf` + 自己的 SDK，import 一个不拉全家。start 后 Register、停机前 Deregister、Effect 回放释放 client，这套生命周期由 appkit 驱动，业务侧只写三行装配代码。破坏性变更只有 import 路径与 `RegistrarRegistry` 的包位置，消费者全部在自家仓库内，跨仓只剩 go-bald-admin 待同步。

> 本文是注册中心域的设计决策文档，与《Bald 日志设计》同族：讲清"为什么是这个方案"，而不只是"这个方案长什么样"。

---

## 背景与动机

### go-wind 的 RegistryAction 把实例丢掉了，注册生命周期没人管

最初我们想直接复用 go-wind 的注册抽象，直到看清它装配侧的写法：builder 返回的是 cleanup，注册出来的实例被 `_ = reg` 丢弃——于是"谁在启动后 Register、谁在停机前 Deregister"没有答案。`bootstrap/registrar.go`（当时在 `pkg/appkit`）的包注释把这一条写成了设计原则：

```go
//  2. Provider 必须返回注册中心**实例**（而非仅 cleanup）——真正的
//     Register/Deregister 生命周期由 appkit 驱动（start 后注册、stopAll
//     前反注册），这是 go-wind RegistryAction（实例被 `_ = reg` 丢弃）
//     缺失的一环。cleanup 只负责 client 生命周期，停机 Effect 回放。
```

**Provider 必须返回实例**，这是本设计与 go-wind 的第一处分叉。抄抽象面，不抄装配缺陷。

### kratos 桥接把 SDK 版本分裂传染给我们

更早的路线是桥接 kratos 的 registry。2026-09-06 我们删掉了它，原因不是抽象不好，而是 kratos contrib 的 nacos 实现同时存在 SDK v1/v2 两套代码路径，我们被迫跟着维护版本判断。多一层间接换来的不是隔离，是把别人的技术债变成自己的。

### 契约挂在主模块上，四个后端为四个名字拖进整棵依赖树

这是本轮改动最硬的一条证据。etcd 后端的契约包只需要 `ServiceInstance` / `Registrar` / `Discovery` / `Watcher` 四个名字（源码 `registry/etcd/contract/contract.go`）：

```go
import (
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	registry "github.com/kalandramo/bald/registry"

	etcd "github.com/kalandramo/bald/registry/etcd"
)
```

在 `registry` 下放之前，中间那行是 `github.com/kalandramo/bald/pkg/registry`——属于主模块。于是后端 `go.mod` 里只能写占位版本，靠 replace 兜底（旧 `contrib/registry/etcd/go.mod`）：

```go
require (
	github.com/kalandramo/bald v0.0.0
	github.com/kalandramo/bald/bconf v0.1.0
	go.etcd.io/etcd/client/v3 v3.7.1
)
```

`v0.0.0` 不是版本，是个占位符。kubernetes 后端更极端，写成了完整的伪版本（`v0.0.0-00010101000000-000000000000`）。四个后端累计 18 处 import `bald/pkg/registry`，每一处都把主模块的整棵依赖树——gin、grpc、cobra、otel、bald-crud/viewer——挂到自己的依赖图上。nacos 后端 `go.mod` 的 indirect 列表里躺着 prometheus / grpc / lumberjack，它们与 nacos SDK 毫无关系，那是主模块的行李。

一个只有 51 行的契约，让四个 SDK 桥接模块的依赖图里都多了一个 Web 框架。这笔账我们不想再付。

---

## 设计

### 契约层：四个类型、零依赖（`registry/registry.go`）

整个 module 的公开面就是一个文件：

```go
// Package registry 定义 bald 框架的服务注册/发现抽象。
//
// 设计理念（对齐 go-wind 的抽象面）：
//   - Registrar 服务注册 + Discovery 服务发现 + Watcher 实例变更监听，
//     不绑定任何具体注册中心（etcd/consul/nacos/kubernetes...）；
//   - 具体后端以直连 SDK 的 provider 形式放 registry/<backend>
//     （独立 module，依赖隔离，按需 import）；
//   - 契约装配走 bootstrap.RegistrarRegistry（显式 MustRegister，fail-fast）；
//   - 提供内存实现（inmemory）用于开发/测试。
package registry

import "context"
```

四个类型：`ServiceInstance`（实例描述：ID / Name / Version / Metadata / Endpoints / Kind）、`Registrar`（`Register` / `Deregister`）、`Discovery`（`GetService` 一次性拉取 + `Watch` 订阅）、`Watcher`（`Next` 阻塞拿全量列表、`Stop` 释放资源）。

边界：`Registrar` 与 `ServiceInstance` 是运行期必须的；`Discovery` / `Watcher` 面向客户端服务发现，目前**没有消费方**，我们仍然保留，理由见「理由与取舍」。契约 module 的 `go.mod` 只有两行，没有 `go.sum`：

```go
module github.com/kalandramo/bald/registry

go 1.27.1
```

### 内存实现：`inmemory` 是契约 module 的同目录子包

`registry/inmemory/` 提供线程安全的 `Registrar`：以 `instance.ID` 为键，同 ID 重复 `Register` 覆盖而非新增，`Deregister` 对不存在 ID 幂等返回，全程 `sync.RWMutex`——连同 `List()` 这个调试扩展方法一共 48 行：

```go
// Register 保存实例（按 ID 覆盖）。
func (r *Registrar) Register(_ context.Context, instance *registry.ServiceInstance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.instances[instance.ID] = instance
	return nil
}

// Deregister 删除实例。
func (r *Registrar) Deregister(_ context.Context, instance *registry.ServiceInstance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.instances, instance.ID)
	return nil
}
```

它只做注册、不做发现——`List()` 是给测试和调试用的，不在契约里。传入 `nil` 实例会 panic，调用方（appkit）保证不传 nil。

### 后端层：一层一个 module，根包保持零契约依赖

每个后端是独立 module，形态统一为"根包纯 SDK 实现 + `contract/` 子包做契约映射"：

```text
registry/etcd/        module github.com/kalandramo/bald/registry/etcd
├── go.mod            require registry + bconf + go.etcd.io/etcd/client/v3
├── registry.go       Registry：Register/Deregister/GetService/Watch
├── watcher.go  service.go  errors.go  options.go
└── contract/contract.go   契约段 → options 映射 + Provider（唯一 import bconf 的地方）
registry/consul/      module .../bald/registry/consul
registry/nacos/       module .../bald/registry/nacos
registry/kubernetes/  module .../bald/registry/kubernetes
```

**为什么 contract 要单独成包**：根包保持零契约依赖，业务只 import 根包时不引入 bconf；只有走契约装配才 import contract。后端的 `go.mod` 从此是真实可解析的版本（`registry/etcd/go.mod`）：

```go
require (
	github.com/kalandramo/bald/bconf v0.1.0
	github.com/kalandramo/bald/registry v0.1.0
	go.etcd.io/etcd/client/v3 v3.7.1
)
```

四个后端都提供双模式构造（对齐 bconfig provider 约定）：

| 构造 | 谁拥有 client 生命周期 | 用途 |
| --- | --- | --- |
| `New(opts...)` | 后端自己（`Close` 关闭） | 契约装配路径 |
| `NewWithClient(c, opts...)` | 调用方（后端不关） | 复用应用内已有连接、测试注入桩 |

各后端「注册」到底做了什么，以及各自的成熟度：

| 后端 | SDK | Register 的实质 | Watch 的实质 |
| --- | --- | --- | --- |
| `etcd` | `go.etcd.io/etcd/client/v3 v3.7.1` | Grant 租约 + `Put` 到 `namespace/name/id`，后台 `KeepAlive` 续约；断链后指数退避重注册（上限 `maxRetry`，默认 5） | `clientv3` watch 前缀 |
| `consul` | `hashicorp/consul/api v1.34.4` | 写服务注册 + TTL 心跳 + 健康检查（`deregister_critical_service_after` 600s） | 1s ticker 轮询 + 索引比对广播（非长轮询） |
| `nacos` | `nacos-sdk-go/v2 v2.3.5` | 按 endpoint 逐个 `RegisterInstance`，服务名带协议后缀 `name.scheme`，`Ephemeral=true`，weight 从 metadata 取 | SDK 订阅回调 |
| `kubernetes` | `k8s.io/client-go v0.37.0` | **不是注册**：把 ID/name/version/metadata/protocols `Patch` 进当前 Pod 的 label 与 annotation | informer 事件过滤 + 全量重发 |

kubernetes 这一行的诚实说明，源码自己就写着（`registry/kubernetes/registry.go`）：

```go
// The Registry simply implements service discovery based on Kubernetes
// It has not been verified in the production environment and is currently for reference only
```

它的 `New` 走 `rest.InClusterConfig()`，集群外必须在注入路径上用 `NewWithClient`；`Deregister` 是把空实例 Patch 回去，等价于清掉那几个 label / annotation——语义上说得通，但请当成参考实现看待。nacos 的 `Close` 是 no-op：SDK v2 未暴露 client 级关闭，ephemeral 实例随连接断开自动注销。

### 装配层：显式注册表 + 按 `registry.type` 单选分发

`RegistrarRegistry` 的语义是"Provider 表 + 单选分发"，Provider 的签名决定了后半段生命周期归谁。**它住在 `bootstrap/registrar.go`**——与 Database/Cache/Storage/Ai/Workflow/Broker 六域 Registry 同一个包（六域变七域），2026-09-17 自 `pkg/appkit` 迁入，理由见「实现与过渡 · 第二刀」：

```go
// RegistrarProvider 按契约 Registry 段构造具体后端。
// 返回 (实例, cleanup, error)：cleanup 释放 client/连接资源（可为 nil），
// 由 appkit FromBootstrap 在停机 Effect 中回放（Deregister 先于它，顺序安全）。
// 各后端的实现见 registry/<backend>/contract 包。
type RegistrarProvider func(ctx context.Context, cfg *bootstrapv1.Registry) (registry.Registrar, func(), error)
```

业务侧的全部代码就是三行：

```go
rr := baldbootstrap.NewRegistrarRegistry()
rr.MustRegister(etcdcontract.Type, etcdcontract.Provider) // 用哪个注册哪个
app, err := appkit.FromBootstrap(cfg, appkit.WithRegistrarRegistry(rr), appkit.WithHTTP(handler))
```

`Build` 的每条出口都是 fail-fast，没有静默路径：

```go
func (r *RegistrarRegistry) Build(ctx context.Context, cfg *bootstrapv1.Registry) (registry.Registrar, func(), error) {
	if cfg == nil {
		return nil, nil, errors.New("bootstrap: registry section is nil")
	}
	typ := cfg.GetType()
	if typ == "" {
		return nil, nil, errors.New("bootstrap: registry.type is empty")
	}
	r.mu.Lock()
	p, ok := r.providers[typ]
	r.mu.Unlock()
	if !ok {
		return nil, nil, fmt.Errorf("bootstrap: registrar provider %q not registered (import the backend contract package and MustRegister it)", typ)
	}
	return p(ctx, cfg)
}
```

装配入口有四条分支，顺序不能变：

| 条件 | 行为 |
| --- | --- |
| 显式 `WithRegistrar(impl)` 实例非 nil | **优先**，跳过契约装配（contract 的 Provider 不被调用） |
| 契约段 `registry` 为 nil | no-op，纯本地运行 |
| 段存在但没接 `WithRegistrarRegistry` | **fail-fast**：配置意图无法兑现，报错而不是静默无注册 |
| 段存在且接了注册表 | `Build` 按 type 分发，注入实例 + 登记停机 Effect `appkit:registrar-client` 释放 client |

`New` 构造路径（不走 `FromBootstrap`）用 `app.SetRegistrar(reg)` 在 `BeforeStart` 里补。时序约束写在方法注释上：必须在 Run 进入 register 之前调用；`register` / `deregister` 每次都读 `a.registrar` 且不缓存，赋值即时生效。

### 生命周期：注册发生在端口真实就绪之后，反注册发生在停机之前

时序是这个设计里最容易写错、也最值钱的部分，全部由 appkit 驱动：

```text
Run:  servers start → waitForEndpoints（:0 解析真实端口）→ Register → afterStart 钩子
                      ↓ 失败：stopAll + 返回错误
停机: ctx 取消/信号 → Deregister（先反注册，避免流量打到将停实例）→ stopAll
                      → Effect 逆序回放（appkit:registrar-client → client.Close）
```

`buildInstance()` 聚合各 `Server.Endpoint()`，推导 `Kind`（单 server 为 `"single"`，否则 `"mixed"`），`Metadata` 固定带 `scheme`（`pkg/appkit/appkit.go`）：

```go
// buildInstance 聚合所有 Server 的 Endpoint 构造 ServiceInstance。
func (a *AppKit) buildInstance() *registry.ServiceInstance {
	var eps []string
	for _, s := range a.servers {
		if ep := s.Endpoint(); ep != "" {
			eps = append(eps, ep)
		}
	}
	kind := "mixed"
	if len(a.servers) == 1 {
		kind = "single"
	}
	return &registry.ServiceInstance{
		ID:        a.id,
		Name:      a.name,
		Version:   a.version,
		Kind:      kind,
		Metadata:  map[string]string{"scheme": kind},
		Endpoints: eps,
	}
}
```

动态端口（`:0`）场景下 `waitForEndpoints` 以 10ms 轮询、上限 5s 等端口绑定完成，宁可启动失败也不把 `scheme://:0` 注册出去。**registry 段不支持热更新**——client 重建侵入性大，变更需重启生效。

### 配置契约：type 单选，子段四个（全部有实现）

契约形状在 bconf（`bconf/proto/bootstrap/v1/registry.proto`）：一个 `type` 字符串 + 四个 `optional` 子消息。

```proto
  string type = 1;

  optional Consul consul = 2;
  optional Etcd etcd = 3;
  optional Nacos nacos = 4;
  optional Kubernetes kubernetes = 8;
  reserved 5, 6, 7, 9;
```

新增后端只需在 `registry/<backend>` 写根包 + contract + 一行 `MustRegister`，不动装配层；要新增配置段则先在 bconf 契约加字段（新字段号）再写实现。**`type` 写错的爆炸点在装配层而非契约层**：`RegistrarRegistry` 查表找不到该 type 的 provider 就 fail-fast（bconf 契约层刻意不硬编码后端清单，与 `LogRegistry` 同口径）。

> 契约瘦身记录（2026-09-17）：删掉预留但**无实现**的 `zookeeper` / `polaris` / `eureka` / `service_comb` 四段（四个 message + 枚举值 + 四个字段；枚举号 4/5/6/8、字段号 5/6/7/9 与对应名字全部 `reserved` 防复用）。与配置契约砍死源（fs/redis/zookeeper/oss/polaris）同口径：**契约只为已实现的后端承诺形状**——未实现的段躺在契约里就是撒谎，删掉比注释掉诚实。删前查证零消费者（Go 侧仅生成代码引用过这些枚举，业务与四个后端均未引用）。

### 改造前 vs 改造后

| 维度 | 改造前 | 改造后 |
| --- | --- | --- |
| 契约位置 | `pkg/registry`（主模块） | `registry/`（独立 module，零依赖） |
| 后端位置 | `contrib/registry/*` | `registry/<backend>`（对齐 cache / broker / log / metrics） |
| 后端 module 名 | `bald-registry-etcd` / `bald/contrib/registry/nacos`（分裂） | 统一 `bald/registry/<backend>` |
| 后端 require | `bald v0.0.0` + `replace ../../..` | `registry v0.1.0`（真实版本） |
| 后端依赖图 | 主模块全家（gin / grpc / cobra / otel） | registry + bconf + SDK |
| 注册生命周期 | 抄 go-wind 会丢实例 | appkit 驱动，实例必返回 |

---

## 理由与取舍

### 为什么 Provider 必须返回实例，而不是只返回 cleanup？

这是 go-wind 的形态，也是最省事的形态（装配层不用管注册）。我们否掉它：注册是整个能力的目的，把目的交给调用方手动调，等于把"忘记 Register"变成默认失败模式。**cleanup 与实例分离**是这条决策的具体形态——cleanup 只管 client 生死，由停机 Effect 回放；实例由 appkit 驱动 Register / Deregister，顺序固定为"Deregister → stopAll → Effect 回放"。

### 为什么契约只留 Registrar 不够，要连 Discovery / Watcher 一起收下？

Discovery / Watcher 目前零消费方，砍掉能让契约更小、风险面更窄。但接口零成本，四个后端已经实现（移植成本已付），而 client-side LB 需求一旦出现，多出来的是一轮破坏性的接口补加。**我们选择现在多留两个接口，而不是以后补一次 breaking。**

### 为什么用显式 `MustRegister` 而不是 init() + blank import 自注册？

隐式注册的代价是依赖图不可见：provider 什么时候进表、谁把它带进来的，只能靠全局搜索。显式注册把"支持哪些后端"变成可 grep 的代码——注册名单写在用户 main 里，注册了什么 import 树里就有什么。未 import 的后端零编译成本，`type` 没注册就在启动时报错：**配置里写了 `registry.type: etcd` 而代码忘了 `MustRegister`，必须炸在启动时，不能静默无注册。** 这条纪律与 bald 全框架一致（日志 `LogRegistry` 同款）。

### 为什么 `type` 是单选，不做多注册中心 fan-out？

`config.Registry` 支持多后端，是因为配置有真实的优先级叠加语义。注册中心没有：一个实例同时注册到两个注册中心，谁消费、以谁为准全是空白。`type` 字段的契约语义就是单选，我们不做没有消费方的多选——`Build` 找不到 type 直接报错，而不是遍历 optional 子消息。

### 为什么 contract 是子包，而后端各自独立 module？

module 是 Go 里依赖隔离的唯一硬边界（同 module 子包的依赖仍进各自的 go.mod）。后端 SDK 重且互不相干（etcd client-go / consul api / nacos SDK / k8s client-go），用 module 边界把依赖代价精确到"用的人付"：import 未用的后端，零依赖零编译成本。contract 单独成包则把 bconf（proto-only）的耦合压在几十行里，后端根包保持纯 SDK 实现，可被直构与单元测试单独使用。

### 为什么 `inmemory` 不拆成独立 module？

它零依赖，拆出去只增加一个 module 和一轮 tag，不带来任何隔离收益。`cache/local` 独立成 module 是因为它依赖 freecache（三方库必须隔离）——这条理由在 inmemory 上不成立。

### 为什么可以接受"没有人用"就先做破坏性迁移？

Discovery / Watcher 没有消费方、注册只在 `_example` 里跑通，这是事实。但"没人用"不是"可以放错位置"的理由——恰恰相反，**趁没有消费方时做破坏性迁移，成本最低**。等 bald-admin 或别的项目接上 client-side LB 之后再动，就要背上兼容包袱。

### 被放弃的方案

| 方案 | 放弃原因 |
| --- | --- |
| 继续留在 kratos registry 抽象上 | contrib 的 nacos 有 SDK v1/v2 分裂，多一层间接换不来隔离，只是把别人的技术债变成自己的；2026-09-06 已删除桥接，不重开 |
| 在主模块留 type alias 兼容垫片 | 消费者全部在自家仓库（主模块 7 文件 + `_example` 两 module + bald-admin 1 处），为不存在的第三方留永久垫片是拿长期成本换短期便利 |
| 只下放契约、后端留在 `contrib/registry` | 契约在顶层、实现在 contrib，与 cache / broker / log / metrics 形态不一致，"后端放哪"会永远得到两个答案 |
| 目录挪到 `registry/` 但不建 go.mod | Go 的模块边界只认 go.mod，顶层目录不含 go.mod 仍是主模块的一部分——依赖隔离收益为零，还破坏"顶层目录 = 独立 module"的既有约定 |
| Provider 只返回 cleanup（go-wind 形态） | 把注册这个目的交给调用方手动调，等于把"忘记 Register"变成默认失败模式 |
| 契约只留 `Registrar`，砍掉 Discovery / Watcher | 接口零成本、后端已实现；砍掉省不下多少，以后补是 breaking |

---

## 兼容性

**这是破坏性变更，我们直说。** 主模块最新 tag 是 v0.6.2，`pkg/registry` 消失这件事随下一个版本（v0.7.0）发布。

| 变更 | 性质 | 代价 |
| --- | --- | --- |
| `.../bald/pkg/registry` → `.../bald/registry` | breaking（import 路径） | 主模块 7 处 + `_example` 两个 module + bald-admin 1 处（待办） |
| `.../bald/pkg/registry/inmemory` → `.../bald/registry/inmemory` | breaking（import 路径） | 同上清单内 |
| `bald-registry-{etcd,consul,kubernetes}` → `.../bald/registry/<backend>` | breaking（module 路径 + 伪版本依赖） | **零消费者**：`v0.0.0` 伪版本靠 replace 兜底，从未发布 |
| `bald/contrib/registry/nacos v0.1.0` → `.../bald/registry/nacos` | breaking（已发布 module 改路径） | bald-admin 是真实消费者（require v0.1.0）；旧 tag 仍可解析，非强制同步 |
| `appkit.NewRegistrarRegistry` → `bootstrap.NewRegistrarRegistry`（类型 `*appkit.RegistrarRegistry` → `*bootstrap.RegistrarRegistry`） | breaking（符号搬家，2026-09-17 迁入 bootstrap） | 本仓 3 处（`_example/bald/register_nacos.go`、appkit 测试、`WithRegistrarRegistry` 签名）已同步；跨仓 bald-admin 已同步（`e9931d1`） |
| 契约与接口语义 | 无变化 | 零 |

四个后端里只有 nacos 发过 tag（`contrib/registry/nacos/v0.1.0`），且它是 bald-admin 的真实依赖，这是本次唯一跨仓库的**路径**破坏点；**符号搬家同样跨仓**——bald-admin 的 T7 装配代码调 `appkit.NewRegistrarRegistry()`，已在升级 `bald v0.7.0` 时一并改为 `bootstrap.NewRegistrarRegistry()`（提交 `e9931d1`）。etcd / consul / kubernetes 从未发版，改名代价仅限本仓内。

迁移路径不设灰度、不做双写：import 路径要么改要么不改，没有中间态。自家仓库一次改完，比留两套路径更安全。契约处于 0.x：`Discovery` / `Watcher` 的形状还可能随首个消费方调整——独立发版让这类调整有版本号可循，比藏在主模块里更可见。

跨 module 使用者注意（非破坏但易踩）：`bald/registry` 是独立 module 且零依赖，require 它本身即可；四个后端各自 require `registry` + `bconf` 两个 module，本地开发时 **replace 必须配齐两条**（`../../registry` 与 `../../bconf`）。gopls 对嵌套 module 常报 BrokenImport 假阳性，以命令行 build / test 为准。

---

## 实现与过渡

### 落地分三步，顺序不可倒置

- [x] **步骤一：目录迁移与 import 改写**（不动任何版本号）——`git mv pkg/registry registry`；`git mv contrib/registry/{etcd,consul,nacos,kubernetes} registry/`；新建两行的 `registry/go.mod`；四个后端 go.mod 改 module 路径与 require，replace 指向 `../../registry` 与 `../../bconf`；改写全部 import（`_example` 必须同步，它是本地 replace）。
- [x] **步骤二：升 require 到真实版本**——兄弟模块 `v0.0.0` → `v0.1.0`（replace 保留本地开发）。**必须在打 tag 之前完成**：tag 指向的提交里若仍写 `v0.0.0`，`v0.0.0 + replace` 模式对外部消费者不可构建（cache 发版轨已踩过此坑）。
- [x] **步骤三：打 tag 并推送**——`registry/v0.1.0` 与四个后端 `v0.1.0` 指向同一提交。

### 发版记录（2026-09-17）

- 提交：迁移落地 `30f2bc5`（refactor，含文档同步与 kratos 遗留描述清理）；发版轨 `48bdc70`（10 个 module 12 处 require `v0.0.0` → `v0.1.0`）；`5baccf9`（发版记录与顺序口径修正）。
- tag：`registry/v0.1.0`、`registry/{etcd,consul,nacos,kubernetes}/v0.1.0`，均为 lightweight，全部指向 `48bdc70`；`main` 与 5 个 tag 已推送 `origin`。
- **tag 内容自洽核对**：`git show <tag>:<module>/go.mod` 确认四个后端 tag 内写的是 `require .../bald/registry v0.1.0` 而非 `v0.0.0`。这是步骤二顺序口径的直接收益。
- **外部可构建性核对**：四个后端仍 `require bconf v0.1.0`（主模块已到 v0.7.1），而本地 replace 会掩盖版本错配。故逐一核对调用点——后端只用 `bconf/gen/go/bootstrap/v1` 的 `Registry` 类型与其子消息访问器，21 个调用点在 v0.1.0 生成代码中全部存在，**已推 tag 外部可构建**，无需重打（重打是 `push -f` 破坏操作）。遗留建议：若要统一 bconf 版本，下次后端有实际改动时一并重打。

- **第二刀发版（同日）**：提交 `e1163a7`（refactor，含 `pkg/appkit/registrar.go` 平移、三个下游模块 replace 补齐与文档整合）；tag `bootstrap/v0.7.2`（内容含 `RegistrarRegistry`，require + replace `bald/registry v0.1.0`）与主模块 `v0.7.0`（`require bootstrap v0.7.2` + `registry v0.1.0`），均为 lightweight、指向同一提交 `e1163a7`；`main` 与 2 个 tag 已推送 `origin`。
- **外部可构建性核对（第二刀，实证而非推断）**：跨仓消费者 bald-admin **零 replace**（纯 tag 依赖）升级到 `bald v0.7.0` + `bootstrap v0.7.2` 后，`go mod tidy` → `go build ./...` → `go vet ./...` → `go test ./...` 全绿（cmd 5.9s、e2e 8.0s）。这就是「先 bootstrap 后主模块」顺序口径的兑现——顺序倒置时 tidy 会直接报新符号不存在（本地 replace 会掩盖，所以必须拿真消费者验一次）。

- **第三刀发版（同日）**：提交 `c272695`（`refactor(bconf)!`，registry 契约删四处无实现预留段 + 四后端 bconf require 统一）；tag `bconf/v0.7.2` 与 `registry/{consul,etcd,kubernetes,nacos}/v0.1.1` 全是 lightweight、指向同一提交；`main` 与 5 个 tag 已推送 `origin`。
- **tag 内容自洽核对（第三刀）**：`git show bconf/v0.7.2:bconf/go.mod`（叶子模块，只 require pflag + protobuf）；`git show registry/nacos/v0.1.1:registry/nacos/go.mod` 得到 `require bald/bconf v0.7.2` + `bald/registry v0.1.0`，不是 `v0.0.0`。
- **外部可构建性核对（第三刀，绕代理实证）**：goproxy.cn 对新 tag 尚未收录（`@v/v0.7.2.info` 直接 404），于是临时用 `GOPROXY=direct` + `GIT_CONFIG_*` 环境变量把 `https://github.com/` 重写为 `git@github.com:`（**不落盘全局 git config**）走 SSH 把新 tag 拉进本地缓存，再在工作区建一个临时消费模块、只 require 这两个 tag：`go mod tidy` → `go build ./...` → `go run .` 打印 `NACOS KUBERNETES`，证明发布版契约里死段确已消失、保留枚举号未变、且 nacos 后端能在 v0.7.2 契约下编译（验证完临时模块已删）。**负向核对**：`bconf@v0.7.2` 的 `registry.pb.go` 中 `Registry_ZOOKEEPER` / `GetZookeeper` 等符号 0 处，描述符里保留 `reserved 4,5,6,8` 与四个保留名。

### 第二刀：`RegistrarRegistry` 迁入 bootstrap（2026-09-17 同日完成）

下放把「是否迁入」的循环约束拆掉了，评估结论是**做，作为独立小步**——归位自洽：产物是"契约段 → 实例"的构造，只依赖 `bconf` + `registry` 两个独立 module，命中 `bootstrap/registry.go` 的归位判别；而运行期编排（start 后 Register、stopAll 前 Deregister、cleanup 挂 Effect）留在 appkit 不变。**这是下放顺带买到的期权**：模块连带为 0——require bootstrap 的 6 个模块在本轮下放中已全部补过 registry replace。

- [x] `pkg/appkit/registrar.go` 平移为 `bootstrap/registrar.go`，错误前缀 `appkit:` → `bootstrap:`，补 `Build` 的生命周期注释（与 CacheRegistry 等六域同款写法）；六域变七域。
- [x] 纯注册表单测（fail-fast 四出口 + 按 type 单选分发）随迁 `bootstrap/registrar_test.go`；appkit 侧 `registrar_test.go` 只留生命周期编排断言（契约装配全流程、显式实例优先、段存在未接表 fail-fast、`SetRegistrar` 路径）。
- [x] `bootstrap/go.mod` 新增 `require .../bald/registry v0.1.0` + `replace .../bald/registry => ../registry`（bootstrap 依赖列表由 4 个 module 变 5 个）。
- [x] 调用方改指新家：`WithRegistrarRegistry(rr *bootstrap.RegistrarRegistry)`（appkit 侧唯一签名改动）、`_example/bald/register_nacos.go`、README / quickstart 示例与各设计文档。
- [x] **不留别名**：`appkit.NewRegistrarRegistry` / `appkit.RegistrarRegistry` 直接消失（0.x 阶段，与 `pkg/registry` 下放同款口径）。appkit 仍 require `registry`——它认识 `registry.Registrar`，是注册生命周期的驱动方。
- [x] **旧账复现一次**：`contrib/{audit-store,audit-stream,observability-otlp}` 三个编译 appkit 的下游模块补 `replace .../bald/bootstrap => ../../bootstrap`——它们 replace 了主模块却没 replace bootstrap，于是「本地 appkit + 已发布 bootstrap v0.7.1」缺符号、编译失败。这与下放那轮的 replace 不传递是同一个坑，换了个面：**改了子模块的公开面，所有本地 replace 主模块且会编译到它的模块都要补 replace**。19 个 replace 主模块的模块已批量复验全绿。（注：`audit-store` 已于 2026-09-24 更名为 `audit-gorm`，此处保留当时的原名。）

### 验证（每个 module 都要跑）

| 模块 | 命令 | 结果 |
| --- | --- | --- |
| `registry` | `go build ./...` + `go vet ./...` + `go test ./...` | 通过（inmemory 测试 4.4s） |
| `registry/{etcd,consul,kubernetes}` | `go build ./...` + `go test ./...` | 通过（无测试文件） |
| `registry/nacos` | `go build ./...` + `go test ./...` | 通过（无测试文件） |
| 根模块 | `go build ./...` + `go test ./pkg/appkit/...` | 通过（appkit 11.2s） |
| `bootstrap`（第二刀新增） | `go build ./...` + `go vet ./...` + `go test ./...` | 通过（bootstrap 5.6s + config 6.8s） |
| `_example` / `_example/bald` | `go build ./...` + `go build -tags nacos ./...` | 通过（含 `-tags nacos`） |
| `transport/{webrtc,tcp}`、`contrib/{store-gorm,observability-otlp,authz-casbin,authn-jwt,audit-stream,audit-store}` | `go build ./...` | 通过（补 replace + require 后） |
| bald-admin（跨仓消费者，零 replace） | `go build ./...` + `go vet ./...` + `go test ./...` | 通过（cmd 5.9s、e2e 8.0s、handler gin/grpc 5.9s/4.9s） |

### 最大风险是漏改一处 import，表现为编译错误而非静默问题

`go build ./...` 在上表每个 module 各跑一遍即可穷尽——import 路径错就是编译错，不会留到运行期。真正需要警惕的是下面这条隐性成本。

**本地 `replace` 不跨 module 传递。** 主模块的 require 图新增 `registry` 后，所有以 `replace github.com/kalandramo/bald => ..` 引入主模块的模块都会报 `updates to go.mod needed`，必须各自补一条 `replace .../bald/registry => ../../registry`。本次实际补齐 10 个（8 个下游 + 2 个 `_example`）。批量修法是先手工加 replace，再跑 `go build -mod=mod ./...` 让 Go 自动补 require。这条成本对任何"从主模块切出子模块"的改动都成立，应进入迁移检查单——**模块数量从 N 到 N+5、发布链路多一轮 tag 与 require 升级，是本次取舍里唯一无法消除的成本，我们接受。**

### 未做（待决定）

- [x] **bald-admin 适配（2026-09-17 完成，提交 `e9931d1`，已推送）**：`backend/go.mod` 升 `bald v0.7.0` / `bootstrap v0.7.2` / `bconf v0.7.1`，`contrib/registry/nacos v0.1.0` → `registry/nacos v0.1.0`（`registry v0.1.0` 转直接依赖）；`cmd/probe` 的 `pkg/registry` import 与 main.go 的 nacos 契约 import 改指新路径，`registrarRegistry()` 改 `bootstrap.NewRegistrarRegistry()`（Option 名与装配形态不变）。见《go-wind-admin 业务移植计划》§8.6 附注。
- [x] **发布顺序（第二刀新增约束，已按序执行）**：`bootstrap/v0.7.2` 先打、主模块 `v0.7.0` 后打并升 `require .../bald/bootstrap v0.7.2`，两个 tag 指向同一提交 `e1163a7`；外部可构建性由 bald-admin 零 replace 升级实证（见上「发版记录」）。
- [x] **契约预留但无实现（2026-09-17 完成）**：删除 `zookeeper` / `polaris` / `eureka` / `service_comb` 四段（四个 message + 四个枚举值 + 四个字段，号与名字 `reserved`）；`buf generate` 已重出 `registry.pb.go`；bconf / 主模块 / bootstrap / `_example`（含 `-tags nacos`）全绿。
- [ ] **Discovery / Watcher 无消费方**：provider 已实现，等 client-side LB 需求。
- [x] **后端 bconf 版本统一（2026-09-17 完成）**：四个后端 `require bald/bconf` v0.1.0 → **v0.7.2**（与本轮契约删除同提交发布），随本次改动一并做。旧 `v0.1.0` tag 内仍写 bconf v0.1.0，故四个后端一并重打 `v0.1.1`——**tag 内容自带真实版本**才是外部可构建的前提（本地 replace 会掩盖版本错配，核对口径见上「发版记录」）。`_example` / `_example/bald` 两个 replace 了 `registry/nacos` 的模块随之把 bconf require 升到 v0.7.2。

---

## 附录

### FAQ

**一个应用能同时注册到多个注册中心吗？**
不能，这是刻意设计。`registry.type` 是单选字段，`Build` 只认一个 Provider——多注册中心扇出没有消费方，也没有被定义过"以谁为准"。

**kubernetes 后端能直接上生产吗？**
先按参考实现看待。它没有真注册（是 Patch 自己的 Pod），`New` 依赖 in-cluster 配置，源码注释自述未在生产验证。要用它，请用 `NewWithClient` 注入 clientset 并自测。

**新增一个注册中心后端要动哪些文件？**
三处，都不在主模块里：①`registry/<backend>` 建独立 module，实现 `Registrar`（建议连同 `Discovery`），提供 `New` / `NewWithClient` / `Close`；②`contract/contract.go` 写契约段 → options 映射 + `Provider` + `const Type`；③业务 main() 两行——`rr := baldbootstrap.NewRegistrarRegistry()` + `rr.MustRegister(<backend>contract.Type, <backend>contract.Provider)`。只想注册、不想发现的实现，也可以跳过契约装配，直接 `appkit.WithRegistrar(impl)` 注入。

**主模块还要不要 require registry？**
要。`pkg/appkit` 是注册生命周期的驱动方，必须认识 `registry.Registrar`。但方向变成主模块 → registry（零依赖），不再有环。

**为什么 registry 的版本号跟主模块解耦？**
因为它可以独立演进：契约处于 0.x，接口还可能调整；而后端发布节奏与主模块无关。独立发版让每次调整都有版本号可循，也让"后端依赖图不含主模块"这件事在版本上被固化。

### 相关文档

- 《Bald 配置契约设计》——`bootstrapv1.Registry` 段所在的 proto 契约层
- 《Bald Bootstrap 设计》——装配层（阶段 A / B）与停机 Effect 回放
- 《框架契约总览》§0（模块依赖层级）——registry 作为独立 module 的依赖方向实证
- 《六域 Registry 迁入 bootstrap》——Registry 归位判别规则的来源
- 《Bald 指标设计》决策③——"契约零依赖 + 后端独立 module"判据的直接出处
