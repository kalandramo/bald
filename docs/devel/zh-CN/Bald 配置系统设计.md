# Bald 配置系统设计

## 1. 定位

`bconfig` 包定义的是 **配置源抽象层 + 组合器**，自身不含任何具体后端实现——后端分布在同级 14 个子目录（file / env / fs / http / etcd / consul / nacos / apollo / polaris / redis / zookeeper / oss / kubernetes / vault），每个子目录是独立 Go module。

一图概括职责划分：

```
┌─ bconfig 包（本文档范围）──────────────────────────┐
│  能力轴（接口）  +  组合器（FallbackReader）        │
└────────────────────────────────────────────────┘
        ↑ 实现                      ↑ 组装
  14 个 provider              上层框架 / 业务
```

## 2. 能力轴与接口契约（`bconfig.go`）

| 轴 | 接口 | 方法 | 可选性 |
|---|---|---|---|
| 读 | `Reader` | `Load(ctx, key) ([]byte, error)` | 必需 |
| 关 | `Closer` | `Close() error` | 可选 |
| 变更通知（信号） | `Watcher` | `Watch(ctx, key) (<-chan struct{}, error)` | 可选 |
| 变更通知（推值） | `ValueWatcher` | `WatchValue(ctx, key) (<-chan []byte, error)` | 可选 |
| 解码 | `Decoder` | `Decode(data []byte, out any) error` | 正交独立 |

预定义的组合名只有两个：`ReadCloser = Reader + Closer`、`ReadWatcher = Reader + Watcher`。

### 2.1 设计要点

1. **接口最小化 + 能力可选发现**。没有 `Provider` 这类大而全的接口，后端按需实现，调用方用**类型断言**在运行期探测能力（见 `fallback.go` 的 `r.(Closer)`、`r.(ValueWatcher)`）。`ValueWatcher` 的注释明确声明它是 *optional*。
2. **两种 watch 语义并存**。
   - `Watcher`：只通知「变了」，收到后仍需 `Load` 回读（多一次 IO，但普适）。
   - `ValueWatcher`：连新字节一起推送（省一次 IO，etcd/consul/nacos 等 SDK 本就能直接给新值）。
3. **`Decoder` 与读/监听完全解耦**。源只吐字节，格式由调用方注入。
4. **可增量实现**：只写 `Load` 也能当配置源用（`env`、`fs` 即如此），不要求一次实现全套。

---

## 3. 契约的形状定义（`bconfig_test.go`）

该文件**不是功能测试**，它定义的是「每个接口的最小方法集」，做两件事：

1. **编译期断言**（`bconfig_test.go:46-54`）：`_ Reader = mockReader{}` 等 7 行，把接口形状钉死——任何人想给接口加方法，这里立刻编译失败。
2. **可组合性断言**（`TestInterfaces_Composable`）：明确三条关系——`ReadCloser` 可作为组合体使用；`ValueWatcher` **可以独立于 `Watcher` 单独实现**；`Decoder` 与读/监听互不依赖。

价值：证明这套抽象支持「渐进实现」，后端可以只实现一部分能力而不破坏契约。

---

## 4. 级联组合器 `FallbackReader`（`fallback.go`）

这是本包唯一有真实行为的组件。用途是多配置源级联回退：比如高优先级的 `env` 覆盖低优先级的 `fs`，或「远程配置中心 + 本地默认值文件」按优先级兜底。

`fallback.go` 里 Load 按优先级取第一个成功源、Close 关所有可关闭子源、WatchValue 合并所有可推值子源的变更。

它的能力由 `fallback_test.go` 逐条固化：

| 能力 | 语义 | 对应测试 |
|---|---|---|
| 多源按序降级 | 按传入顺序依次 `Load`，第一个「无 error 且 data 非 nil」者胜出 | `Load_FirstSucceeds` / `Load_FallbackToSecond` / `Load_SkipErrors` |
| 失败聚合 | 全失败 → `errors.Join` 所有子错误；全返回 nil（无 error）→ 报 `no source could resolve key` | `Load_AllFail` / `Load_AllNil` |
| 生命周期传递 | `Close()` 遍历子源，只关实现了 `Closer` 的，聚合错误 | `Close_AllClosers` / `Close_SkipsNonClosers` / `Close_JoinErrors` |
| 多源 watch 合并 | 把所有实现了 `ValueWatcher` 的子源合并为**一个** channel | `WatchValue_*` |
| 空能力显式报错 | 无子源实现 `ValueWatcher` → 明确报错，而非静默「永不触发」 | `WatchValue_NoWatchers` |
| ctx 取消即关 channel | `cancel()` 后输出 channel 被 close | `WatchValue_ContextCancel` |


### 4.1 最关键的一条语义

`fallback.go` 的 `WatchValue`：任一子源推送变更时，**丢弃推送来的值，重新走一遍 `Load` 全链路**，再把「重算后的有效值」转发。

测试 `WatchValue_MultipleWatchers` 正是验证这一点：低优先级源 r2 变更并推送通知，但高优先级源 r1 仍有值 → 合并 channel 推出的仍是 `"a"`（r1 的值）；只有 r1 变更时才推出 `"a-updated"`。

> 若直接用推送值转发，多源**遮蔽（shadowing）语义就会崩**。

这条可推广为一个通用原则：
**永远不要把「事件里带着的值」当真值，只把事件当作「该重算了」的触发器。**

### 4.2 工程细节

- **防 goroutine 泄漏**：先收集齐所有 sub channel 再起 goroutine（`fallback.go` 注释明写），中途某个 `WatchValue` 失败即整体返回错误。
- **构造即校验**：`NewFallbackReader()` 无参返回 error，而不是造出一个「永远失败」的对象。
- 输出 channel 缓冲为 1：合并后不保证每个源事件都一一送达（合并/去重语义），慢消费者下的新值会覆盖旧值。

---

## 5. 接口组合矩阵与缺口

### 5.1 矩阵

含 `Reader` 的组合空间为 `2 × 2 × 2 = 8` 格（Closer / Watcher / ValueWatcher 各有或无）：

| 组合 | 是否命名 | 实际存在 |
|---|---|---|
| `Reader` | ✅ | `env`、`fs` |
| `Reader+Closer` | ✅ `ReadCloser` | — |
| `Reader+Watcher` | ✅ `ReadWatcher` | — |
| `Reader+ValueWatcher` | ❌ | 多数 provider |
| `Reader+Closer+Watcher` | ❌ | — |
| `Reader+Closer+ValueWatcher` | ❌ | **`FallbackReader` 自身形态** |
| `Reader+Watcher+ValueWatcher` | ❌ | — |
| 三者全有 | ❌ | — |

8 格只命名了 2 格。`FallbackReader` 落在第 6 格，因此 `fallback.go` 的编译期断言只能拆成三行核心声明（`Reader` / `Closer` / `ValueWatcher`）+ 一行冗余的 `ReadCloser`——后者已由 `Reader`+`Closer` 保证，写它纯为声明意图。

### 5.2 缺口的真实代价

**先明确：代价不是「不能用」。** Go 的接口是隐式满足 + 结构化类型，组合接口名只是**类型别名**，零运行时成本、零语义贡献。`FallbackReader` 该有的能力一个不少。

实际影响只有三处，都不严重：

1. **声明啰嗦**：本可一行 `_ ReadCloserValueWatcher = (*FallbackReader)(nil)`，现在写四行。
2. **形参无法一个词表达**：需要「可关闭 + 能推值监听的源」时，签名只能内联 `interface{ Reader; Closer; ValueWatcher }`。
3. **契约不可读**：读者无法从接口清单看出「成熟 provider 应有的形态」，只能从断言反推。

**判断标准：只有当某个函数/字段的形参需要它时才命名组合。** 当前调用点是 `NewFallbackReader(readers ...Reader)` —— 入参是最窄的 `Reader`，内部靠类型断言升级能力。按此标准，`ReadCloserValueWatcher` **现在不需要存在**，缺口是良性的。

（Go 生态先例支持「按需命名」：`io` 包命名了 `ReadCloser`/`ReadWriteCloser`/`ReadSeekCloser`/`ReadWriteSeekCloser` 等一大串，缺失的组合就地用匿名接口。补名不违背惯例，但也非必须。）

### 5.3 比命名缺口更值得关注：signal 源被静默丢弃，以及落地方式

只支持信号通知的后端无法直接被组合层监听——由 **provider 内部自行回读**（收到信号后调一次 `Load`，再以 `ValueWatcher` 形式暴露）。能力转换下沉到后端，框架只认一种契约。

最终落地即方案：无需任何代码逻辑改动，仅修正以下几处注释：

- `bconfig.go` 的 `Watcher`：声明能力边界——组合层只合并 `ValueWatcher`，不识别本接口；并给出指引「信号型后端请在 provider 内部 Load 回读后转推值」。
- `bconfig.go` 的 `ReadWatcher`：补一句「组合层不识别本组合，仅作类型便利」。
- `bconfig.go` 的 `ValueWatcher`：把「仅支持信号模式的通知者实现 `Watcher` 即可」改为「本接口才是组合层唯一识别的监听契约」。
- `fallback.go` 的 `WatchValue`：补一句「只合并 `ValueWatcher`，不引入适配器去兼容信号源，转换责任下沉到 provider」。

这同样贴合 §4.1 的结论：既然信号与推值最终都要「重新 `Load` 才敢用」，就不在框架层为统一两种模式引入额外抽象，转换交给 provider 自己完成。

### 5.4 抗组合爆炸的真正手段：组合器嵌套

`_ Reader = (*FallbackReader)(nil)` 这条断言意味着：

**`FallbackReader` 自己也是 `Reader`，因此可以被塞进另一个 `FallbackReader`。**

```go
// 远程组：etcd 兜底 consul
remote, _ := NewFallbackReader(etcdSrc, consulSrc)
// 再与本地文件组成最终链：本地 > 远程组
all, _ := NewFallbackReader(fileSrc, remote)
```

这才是「组合矩阵爆炸」的标准答案：**不为每种组合命名，而是让组合器自己实现接口，用嵌套表达任意组合。**


## 6. 后端实现矩阵

| 能力组合 | provider |
|---|---|
| `Load` + `WatchValue` | etcd / consul / nacos / apollo / polaris / redis / zookeeper / oss / vault / kubernetes |
| `Load` + `WatchValue` + `Close` | file、http |
| 仅 `Load` | env、fs |

注：vault 在此体系中只是**一个普通配置源**，不是特殊通道——「敏感配置」被降维成「换个后端」。

---
