# Bald 注册中心设计：契约零依赖、后端各自独立 module、装配只认显式注册

> Author(s): bald 团队
>
> Last updated: 2026-10-30
>
> Discussion at: 源码 `registry/`、`registry/<backend>/contract/provider.go`、`pkg/appkit/bootstrap.go`（`storeProductWith`）
>
> Status: Accepted

## 摘要

服务注册/发现被拆成三层：**契约**（`registry` module，四个类型，只 import `context`）、**后端**（`registry/<backend>`，一个后端一个独立 module，直连各自 SDK）、**装配**（`contract.AppProvider` 声明 `registry.<backend>` 段，产物落引擎的注册中心位；appkit 驱动注册/反注册的生命周期）。**最重要的承诺：契约本身零依赖，后端根包不碰主模块。** 契约就是 `registry.go` 一个文件 51 行，`go.sum` 是空的；后端根包各自 require `registry` + 自己的 SDK，import 一个不拉全家（`contract/` 子包为实现段驱动 Provider 而 import bconf + appkit，代价见「依赖边界」）。start 后 Register、停机前 Deregister、Effect 回放释放 client，这套生命周期由 appkit 驱动，业务侧只写一行 `Providers(...)`。装配是**段驱动**：启用的表达是契约里出现 `registry.<backend>` 段，代码里 `Providers(...)` 带对应 Provider——两边缺一边都在启动期 fail-fast。

> 本文是注册中心域的设计决策文档，与《Bald 日志设计》同族：讲清"为什么是这个方案"，而不只是"这个方案长什么样"。

---

## 背景与动机

### 只返回 cleanup 的注册抽象把实例丢掉了，注册生命周期没人管

最初我们想直接复用现成的注册抽象，直到看清它装配侧的写法：builder 返回的是 cleanup，注册出来的实例被 `_ = reg` 丢弃——于是"谁在启动后 Register、谁在停机前 Deregister"没有答案。`bootstrap/registrar.go`（当时在 `pkg/appkit`）的包注释把这一条写成了设计原则：

```go
//  2. Provider 必须返回注册中心**实例**（而非仅 cleanup）——真正的
//     Register/Deregister 生命周期由 appkit 驱动（start 后注册、stopAll
//     前反注册），这是只返回 cleanup 的注册抽象（实例被 `_ = reg` 丢弃）
//     缺失的一环。cleanup 只负责 client 生命周期，停机 Effect 回放。
```

**Provider 必须返回实例**，这是本设计的第一处分叉：抄抽象面，不抄装配缺陷。

### 桥接第三方 registry 把 SDK 版本分裂传染给我们

更早的路线是桥接第三方 registry 体系。我们删掉了它，原因不是抽象不好，而是它的 nacos 实现同时存在 SDK v1/v2 两套代码路径，我们被迫跟着维护版本判断。多一层间接换来的不是隔离，是把别人的技术债变成自己的。

### 契约挂在主模块上，四个后端为四个名字拖进整棵依赖树

契约层的独立性有一条硬证据：etcd 后端的契约包只需要 `ServiceInstance` / `Registrar` / `Discovery` / `Watcher` 四个名字（源码 `registry/etcd/contract/contract.go`）：

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
// 设计理念：
//   - Registrar 服务注册 + Discovery 服务发现 + Watcher 实例变更监听，
//     不绑定任何具体注册中心（etcd/consul/nacos/kubernetes...）；
//   - 具体后端以直连 SDK 的 provider 形式放 registry/<backend>
//     （独立 module，依赖隔离，按需 import）；
//   - 契约装配走各后端 contract.AppProvider（注册 registry.<backend> 段）；
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

### 装配层：段驱动 Provider（`registry.<backend>`）

registry 域在装配上**没有独立的注册表**：每个后端的 `contract` 子包导出一个
`AppProvider`，声明自己消费 `registry.<backend>` 子段，构建产物是
`registry.Registrar`。引擎按段路径做**末者胜**（后声明的 Provider 接管同一
路径），段缺失则跳过——启用的表达就是「契约里有这个子段 + 代码里带了这个
Provider」。

```go
appkit.MustServe(ctx, append(std.Standard(),
	etcdcontract.AppProvider(), // registry.etcd 段 → registry.Registrar
	)..., appkit.WithHTTP(handler))
```

契约形状（`registry.type` 是描述字段，不参与分发）：

```yaml
registry:
  type: etcd
  etcd:
    endpoints: ["127.0.0.1:2379"]
```

三个必须记住的语义：

| 事实 | 后果 |
| --- | --- |
| **消费由子段存在性决定** | `registry.etcd` 段在即建；`type` 写什么都只影响描述（`type` 与子段不一致属配置错误，但不改变装配对象） |
| **段在而无 Provider 接管 = 启动期报错** | L3 段覆盖检查把顶层 `registry` 判为「无消费者」并列出修复提示——配置意图无法兑现不静默 |
| **程序化实例优先** | `appkit.Registrar(impl)` 声明显式实例时，registry 节点以 Override 进计划、纯 registry Provider 被跳过（程序化接管 = 刻意覆盖，计划里可见理由） |

`AppProvider` 的 `Plan` 节点固定为 `PhaseBootstrap` / `PriorityRegistry` /
`ReloadRestart` / `EffectName: "appkit:registrar-client"`：注册中心在日志之后、
业务客户端之前构建；client 重建侵入性大，注册中心变更需重启生效；cleanup
挂在 `appkit:registrar-client` Effect 上，Deregister 先于它执行。

段路径本身是名单：`Providers(...)` 里 import 了哪个 `contract` 包，就有哪个
后端可用。新增后端的动作是「写根包 + contract（含 `AppProvider`）」，装配层
零改动。

### 依赖边界：根包零契约、contract 付 appkit

```text
registry/                 零依赖（go.sum 为空）
registry/inmemory/        零依赖
registry/<backend>/       纯 SDK 实现 + registry 依赖，不 import bconf
registry/<backend>/contract/  import bconf + appkit（段驱动 Provider 的实现面）
```

**import `registry/etcd`（根包）不会拉入主模块**；只有 import
`registry/etcd/contract` 才会——因为 `AppProvider` 的类型签名是
`appkit.Provider`。这是段驱动装配的既定代价（全仓同款：`cache/redis`、
`oss/*`、`transport/*` 的 contract 包同样 import appkit），换来的是业务侧
零样板：一行 `AppProvider()` 替代一段装配代码。只要 SDK 直构，依赖图保持
干净。

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

### 配置契约：四个子段，一个描述字段

契约形状在 bconf（`bconf/proto/bootstrap/v1/registry.proto`）：一个 `type` 字符串 + 四个 `optional` 子消息。

```proto
  string type = 1;

  optional Consul consul = 2;
  optional Etcd etcd = 3;
  optional Nacos nacos = 4;
  optional Kubernetes kubernetes = 8;
  reserved 5, 6, 7, 9;
```

`type` 是**描述字段**，不参与分发：装配看子段存在性。二者不一致（`type: etcd`
而只有 `consul` 段）不会改变装配对象，属于配置错误；契约层不硬编码后端清单，
也不做 cross-field 校验。

新增后端的动作：`registry/<backend>` 写根包（纯 SDK）+ `contract/`（契约段 →
options 映射 `Provider` + `const Type` + 段驱动 `AppProvider`）；要用新配置段则
先在 bconf 加字段（新字段号）。装配层零改动。

序号 5/6/7/9 保留：`zookeeper` / `polaris` / `eureka` / `service_comb` 四段曾经
声明、从未实现，已整体删除（名字与号一并 reserved 防复用）。**契约只为已实现
的后端承诺形状**——未实现的段躺在契约里就是撒谎。

---

## 理由与取舍

### 为什么 Provider 必须返回实例，而不是只返回 cleanup？

这是最省事的形态（装配层不用管注册）。我们否掉它：注册是整个能力的目的，把目的交给调用方手动调，等于把"忘记 Register"变成默认失败模式。**cleanup 与实例分离**是这条决策的具体形态——cleanup 只管 client 生死，由停机 Effect 回放；实例由 appkit 驱动 Register / Deregister，顺序固定为"Deregister → stopAll → Effect 回放"。

### 为什么契约只留 Registrar 不够，要连 Discovery / Watcher 一起收下？

Discovery / Watcher 目前零消费方，砍掉能让契约更小、风险面更窄。但接口零成本，四个后端已经实现（移植成本已付），而 client-side LB 需求一旦出现，多出来的是一轮破坏性的接口补加。**我们选择现在多留两个接口，而不是以后补一次 breaking。**

### 为什么用显式 Provider 而不是 init() + blank import 自注册？

隐式注册的代价是依赖图不可见：谁把 provider 带进来的只能靠全局搜索。段驱动
装配把「支持哪些后端」变成可 grep 的两处代码——import 列表里有哪个 `contract`
包，`Providers(...)` 里就有哪个 `AppProvider`。未 import 的后端零编译成本；
**配置里写了 `registry.etcd` 段而代码没带对应 Provider，L3 覆盖检查在启动期
报错**，不会静默无注册。

### 为什么注册中心是单选，不做多注册中心 fan-out？

`config` 段支持多源，是因为配置有真实的优先级叠加语义。注册中心没有：一个
实例同时注册到两个注册中心，谁消费、以谁为准全是空白。因此两条约束都落在
计划期：

- 契约同时出现两个 registry 子段且两个 Provider 都在 → 编译期报错
  `registry is single-select`（后建者静默覆盖先建者是不可接受的失败模式）；
- `type` 只描述、不分发，不存在「按 type 遍历」的扇出路径。

要换注册中心：改契约（留一个子段）+ 换 Provider，重启生效（`ReloadRestart`）。

### 为什么 contract 是子包，而后端各自独立 module？

module 是 Go 里依赖隔离的唯一硬边界（同 module 子包的依赖仍进各自的 go.mod）。后端 SDK 重且互不相干（etcd client-go / consul api / nacos SDK / k8s client-go），用 module 边界把依赖代价精确到"用的人付"：import 未用的后端，零依赖零编译成本。contract 单独成包则把 bconf（proto-only）的耦合压在几十行里，后端根包保持纯 SDK 实现，可被直构与单元测试单独使用。

### 为什么 `inmemory` 不拆成独立 module？

它零依赖，拆出去只增加一个 module 和一轮 tag，不带来任何隔离收益。`cache/local` 独立成 module 是因为它依赖 freecache（三方库必须隔离）——这条理由在 inmemory 上不成立。

### 为什么在消费方到来之前就定下契约形状？

Discovery / Watcher 目前没有消费方、注册只在 `_example` 里跑通，这是事实。但「没人用」不是「可以放错位置」的理由——恰恰相反，**在没有消费方时定型，成本最低**。等 bald-admin 或别的项目接上 client-side LB 之后再改形状，就要背上兼容包袱。

### 被放弃的方案

| 方案 | 放弃原因 |
| --- | --- |
| 继续桥接第三方 registry 抽象 | 其 nacos 实现有 SDK v1/v2 分裂，多一层间接换不来隔离，只是把别人的技术债变成自己的；桥接已移除，不重开 |
| 在主模块留 type alias 兼容垫片 | 消费者全部在自家仓库（主模块 7 文件 + `_example/bald` + bald-admin 1 处），为不存在的第三方留永久垫片是拿长期成本换短期便利 |
| 只下放契约、后端留在 `contrib/registry` | 契约在顶层、实现在 contrib，与 cache / broker / log / metrics 形态不一致，"后端放哪"会永远得到两个答案 |
| 目录挪到 `registry/` 但不建 go.mod | Go 的模块边界只认 go.mod，顶层目录不含 go.mod 仍是主模块的一部分——依赖隔离收益为零，还破坏"顶层目录 = 独立 module"的既有约定 |
| Provider 只返回 cleanup（最省事形态） | 把注册这个目的交给调用方手动调，等于把"忘记 Register"变成默认失败模式 |
| 契约只留 `Registrar`，砍掉 Discovery / Watcher | 接口零成本、后端已实现；砍掉省不下多少，以后补是 breaking |

---

## 兼容性

契约处于 0.x，接口形状随首个 client-side LB 消费方仍可能调整。

**装配面**：不存在进程级全局 `RegistrarRegistry` / `WithRegistrarRegistry`。注册中心经各后端 `contract.AppProvider()`（`appkit.Providers(...)`）+ 契约 `registry.<backend>` 段装配；`registry.type` 仅作描述，装配看子段存在性。

后端模块与 tag：

| module | 说明 |
| --- | --- |
| `github.com/kalandramo/bald/registry` | 契约，零依赖 |
| `registry/{etcd,consul,nacos,kubernetes}` | 各自独立 module；根包纯 SDK，`contract/` 子包做契约映射与段驱动 Provider |

**跨 module 使用注意**：

- 只用 SDK：require `registry/<backend>` 本身即可（根包不 import bconf，也不
  import 主模块）。
- 走契约装配：require `registry/<backend>/contract`——该子包 import appkit
  （主模块），依赖图因此包含主模块。这是 `AppProvider` 类型签名的必然代价，
  与 `cache/redis`、`oss/*`、`transport/*` 的 contract 包同款。
- 本地开发时 `replace` 不跨 module 传递：require 了多个本地模块的工程须逐条
  补 `replace`（`../../registry`、`../../bconf`、`../../bootstrap` 等），否则
  `go build` 报 `updates to go.mod needed`。gopls 对嵌套 module 常报
  BrokenImport 假阳性，以命令行 build / test 为准。
- 发版顺序：四个后端现在 require 主模块（`AppProvider` 面），先打主模块 tag，
  再升后端 require 打后端 tag；tag 指向的提交里必须是真实版本号，不能留
  `v0.0.0` + replace（对 tag 内构建无效）。

---

## 实现状态

- [x] 契约四类型 + `inmemory` 实现（`registry/registry.go`、`registry/inmemory`）。
- [x] 四个后端：`etcd` / `consul` / `nacos` / `kubernetes`，各自独立 module，
      `New(opts...)` 自持 client / `NewWithClient` 复用注入。
- [x] 段驱动装配：每个后端 `contract/` 同时导出契约映射 `Provider` 与
      `AppProvider`（`registry.<backend>` 段 → `registry.Registrar`）。
- [x] 生命周期编排：register 在端口就绪后、deregister 在停机前、cleanup 挂
      `appkit:registrar-client` Effect；程序化 `appkit.Registrar(impl)` 接管。
- [x] registry 单选在计划期强制（双 Provider 触发即编译错误）。
- [x] 契约瘦身：四个无实现预留段删除，号与名 reserved。
- [ ] **Discovery / Watcher 无消费方**：实现已在，等 client-side LB 需求。
- [ ] **kubernetes 为参考实现**：`New` 依赖 in-cluster 配置，未在生产验证。

验证（每个 module 各跑一遍即可穷尽 import/依赖问题）：

```bash
# 契约 + 四个后端（含 contract 包）
for m in registry registry/etcd registry/consul registry/nacos registry/kubernetes; do
  (cd $m && go build ./... && go vet ./... && go test ./...)
done
# 根模块：生命周期编排与计划期单选
go test ./pkg/appkit/...
```

---

## 附录

### FAQ

**一个应用能同时注册到多个注册中心吗？**
不能。registry 是单选域：契约同时出现两个子段且两个 Provider 都在时，计划期
直接报错（`registry is single-select`），不会出现后建者静默覆盖先建者。

**kubernetes 后端能直接上生产吗？**
先按参考实现看待。它没有真注册（是 Patch 自己的 Pod），`New` 依赖 in-cluster
配置，源码注释自述未在生产验证。要用它，请用 `NewWithClient` 注入 clientset
并自测。

**新增一个注册中心后端要动哪些文件？**
三处，都不在主模块里：①`registry/<backend>` 建独立 module，实现 `Registrar`
（建议连同 `Discovery`），提供 `New` / `NewWithClient` / `Close`；②
`contract/contract.go` 写契约段 → options 映射 + `Provider` + `const Type`，
`contract/provider.go` 写 `AppProvider`（段路径 `registry.<backend>`，节点固定
`PhaseBootstrap` / `PriorityRegistry` / `ReloadRestart`，EffectName
`appkit:registrar-client`）；③业务 main() 一行——`appkit.Providers(
<backend>contract.AppProvider())`。只想注册、不想发现的实现，也可以跳过契约
装配，直接 `appkit.Registrar(impl)` 注入。

**主模块还要不要 require registry？**
要。`pkg/appkit` 是注册生命周期的驱动方，必须认识 `registry.Registrar`；方向
是主模块 → registry（零依赖），无环。

**为什么 registry 的版本号跟主模块解耦？**
契约与后端可以独立演进：契约 0.x 仍可能调整，后端发布节奏与主模块无关。独立
发版让每次调整都有版本号可循。注意解耦的是**版本**，不是依赖——`contract`
子包为段驱动 Provider 依赖主模块，只有根包保持无主模块依赖。

### 相关文档

- 《Bald 配置契约设计》——`bootstrapv1.Registry` 段所在的 proto 契约层
- 《Bald 声明式装配设计》——段驱动的装配计划、阶段与停机（`TeardownAfter`）
- 《框架契约总览》§0（模块依赖层级）——registry 作为独立 module 的依赖方向实证
- 《Bald 指标设计》决策③——"契约零依赖 + 后端独立 module"判据的直接出处
