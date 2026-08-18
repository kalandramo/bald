# bald 配置中心设计文档

> 状态：设计中（核心抽象与 5 项决策已落地到 `pkg/config`，见第 7 节）
> 最后更新：2026-08-18
> 包路径：`pkg/config`（配置加载层）

---

## 1. 背景与问题

bald 当前（`pkg/config`）基于 viper 标准机制实现了「本地文件 + 环境变量 + flag + 远程配置中心」，
但 viper 的远程配置（remote）能力存在硬性缺陷，无法直接用于生产级配置中心：

| 问题 | 说明 |
|---|---|
| 强制 JSON | viper remote 内部用 `json.Unmarshal` 解析远程字节，etcd/consul 里存 yaml 会直接报错 |
| watch 不可靠 | `WatchRemoteConfigOnChannel` 依赖各 provider 自行实现 `Watcher`，官方只 coverage etcd/consul/firestore，且经全局 channel 推送、无法精确区分 key，常丢事件 |
| 无鉴权/TLS 透传 | `AddRemoteProvider` 注册时拿不到 client 配置（证书、token），复杂环境不可用 |
| 格式单一、合并弱 | 远程只覆盖不叠加，缺少多源优先级与多环境 |

> 结论：**viper remote 机制不可靠，业界框架（Kratos 等）均绕开它、自建 Source 抽象。**
> 因此本设计采用「Kratos 的 Source 抽象」+「viper 作为壳」的混合方案：既解决 viper remote 的全部痛点，
> 又满足「配置加载与热更新仍使用 viper」的诉求。

---

## 2. 设计目标

1. 支持**本地配置 + 远程配置中心**混合。
2. 配置文件格式支持 **json / yaml**（远程字节格式由后端自声明，不强制 JSON）。
3. 支持**多环境**（dev/test/prod 等）。
4. 配置加载与热更新**仍基于 viper**（业务继续用 `app.Viper().Unmarshal`）。
5. 远程 watch 可靠，变更能稳定触发 `OnConfigChange`。

---

## 3. 核心抽象：`RemoteSource`

借鉴 Kratos `config.Source`：把「远程存储」抽象为一个接口，每个后端自己负责把远程数据
适配成「原始字节 + 格式」。格式不再由 viper 决定，而由后端声明，从而绕开强制 JSON。

```go
// pkg/config/source.go（设计）

// RemoteSource 表示一个远程配置源。
// 每个后端（etcd/consul/nacos/apollo...）实现该接口，自行声明字节格式。
type RemoteSource interface {
    // Read 拉取一次远程配置，返回原始字节与格式（"json"/"yaml"）。
    Read(ctx context.Context) (data []byte, format string, err error)

    // Watch 监听远程变更，每次变更通过 onChange 推送最新字节与格式。
    // ctx 取消即停止监听。
    Watch(ctx context.Context, onChange func(data []byte, format string)) error
}
```

与 Kratos 的差异：Kratos 的 `Source.Watch()` 返回 `Watcher`，由 reader 主循环 `Next()` 重新 `Load`；
本设计改为 **直接回调 `onChange`（push 模式）**，更省一次 IO，且与 viper 注入流程衔接更自然
（对齐 go-lulu 的 `ValueWatcher.WatchValue` 推值思路）。

### 3.1 后端示例：etcd

```go
type etcdSource struct {
    client *clientv3.Client
    path   string
    prefix bool
}

func (s *etcdSource) Read(ctx context.Context) ([]byte, string, error) {
    rsp, err := s.client.Get(ctx, s.path, clientv3Op(s.prefix))
    if err != nil {
        return nil, "", err
    }
    // 格式由 path 后缀声明：/config/demo.yaml → yaml
    return rsp.Kvs[0].Value, formatOf(s.path), nil
}

func (s *etcdSource) Watch(ctx context.Context, onChange func([]byte, string)) error {
    ch := s.client.Watch(ctx, s.path, clientv3Op(s.prefix))
    go func() {
        for resp := range ch {
            if resp.Err() != nil {
                return
            }
            for _, ev := range resp.Events {
                if ev.Type == clientv3.EventTypePut {
                    onChange(ev.Kv.Value, formatOf(s.path))
                }
            }
        }
    }()
    return nil
}
```

nacos/apollo 类似，只是 `Read` 用其 SDK 的 `GetConfig(DataId, Group)`，`Watch` 用 SDK 的 `ListenConfig` 回调。
具体桥接写法见 `cmd/bald/main.go` 与 `pkg/config/integration_test.go`（nacos 真实 API：
先 `nacosclients.NewConfigClient(vo.NacosClientParam{...})` 得到 `IConfigClient`，
再 `nacosconfig.NewConfigSource(client, WithDataID(...), WithGroup(...))`；
`dataID` 带扩展名（如 `.yaml`）以便 contrib 正确识别格式）。

---

## 4. 加载流程（绕过 viper 强制 JSON）

关键技巧：**远程字节不经过 viper 的 `AddRemoteProvider`，而是手动注入 viper**。

```go
// 远程字节 → viper（绕开 viper 强制 JSON 的限制）
func injectRemote(v *viper.Viper, data []byte, format string) error {
    v.SetConfigType(format)                  // "yaml" / "json"
    return v.ReadConfig(bytes.NewReader(data)) // 按声明格式解析
}
```

多源合并顺序（优先级：高 → 低）：

```
命令行 flag（--config / 业务 flag）
   > 本地文件（覆盖远程基准）
      > 环境变量（NAME_ 前缀）
         > 远程配置中心（基准源）
```

即：**远程配置作为基准，本地文件覆盖，flag 最高优先级**（推荐路线；亦可按需反转）。

### 4.1 `Load` 流程

> 关键实现细节：viper 有 **override 层（最高优先级）** 与 **底层 config map** 两层。
> 为同时满足「远程基准 + 本地覆盖」且不被 watch 污染，采用：
>   - **远程** 与 **本地** 都落在 viper 的**底层 config**（低于 flag / env 层），但加载顺序上
>     远程先 `ReadConfig`、本地后 `MergeConfigMap`，同名 key **本地后写赢**，从而本地覆盖远程；
>   - **flag**（`BindPFlags`）与 **env**（`AutomaticEnv`，`NAME_` 前缀）位于更上层，天然压过底层，
>     优先级正确：**flag > 本地 > env > 远程**；
>   - 远程 watch 重新 `injectRemote`（Reset 底层）后**重新叠加本地 map**，因此本地不会被污染；
>   - 本地 watch 重新解析本地文件并 `MergeConfigMap`，远程基准保持不变。
> 这样本地始终覆盖远程，flag/env 始终压本地，且两者热更新互不污染。

```
Load(Options):
  1. 初始化 viper；绑定 flag / 环境变量（NAME_ 前缀）；主 v SetConfigFile（仅设路径）
  2. 若配置了 RemoteSource：data, format, _ := src.Read(ctx); injectRemote(v, data, format)  // 远程写入底层（基准）
  3. 本地覆盖：localMap := parseLocal(opts); v.MergeConfigMap(localMap)  // 本地在底层后写赢 > 远程
  4. 若 Watch：
       本地：v.WatchConfig（仅当本地文件存在），变更回调内重新 parseLocal + MergeConfigMap
       远程：src.Watch(ctx, func(d, f){
               injectRemote(v, d, f)        // 重置底层
               v.MergeConfigMap(localMap)   // 重新叠加本地覆盖（缓存的 localMap）
               OnChange(v) })
```

### 4.2 踩坑记录：本地覆盖远程的层级选择与 flag 优先级（CR Issue#1 及回归测试补充）

> 来自 `@command://cr` 审查发现的 🔴 功能正确性 bug，并在后续补充单元测试时进一步修正（flag 必须高于本地）。此处沉淀为经验。

**版本一（初版，错误）**：把远程放进 override 层、本地放进底层——两层装反，违背决策①「远程基准 + 本地覆盖」，测试 `TestLoad_RemoteBaselineThenLocalOverride` 抓到回归（远程 `:8080` 压住本地 `:9090`）。

**版本二（第一版修复，仍错误）**：改为本地进 override 层、远程进底层。本地确实覆盖远程了，但**漏掉了 flag**。viper 中 `v.Set`（override 层）优先级高于 `BindPFlags` 的 flag 层，导致 `--http.addr` 无法压过本地文件——违反「flag > 本地」。补充测试 `TestLoad_FlagHighestPriority` 抓到此回归。

**版本三（最终正确）**：远程与本地**都落在底层 config**，靠加载顺序决定胜负——远程先 `ReadConfig`、本地后 `MergeConfigMap`，同名 key 本地后写赢。flag（`BindPFlags`）与 env（`AutomaticEnv`）在更上层，自然压过底层：

| 层（高 → 低） | 内容 | 结果（同 key：flag `:7000` / 本地 `:9090` / 远程 `:8080`） |
|---|---|---|
| override 层 | （无写入） | — |
| flag 层 | `BindPFlags` | `:7000`（最高业务优先级） |
| env 层 | `AutomaticEnv`（`NAME_` 前缀） | 可覆盖底层，但低于 flag |
| 底层 config | 远程先写 + 本地后 `MergeConfigMap` | `:9090`（本地赢远程，作为基准兜底） |

**关键约束（被测试揭示）**：
1. viper 的 flag **只有显式 Parse 且有值才生效**，默认值不覆盖 config 层。因此测试必须 `fs.Parse([]string{"--http.addr=:7000"})`，真实运行依赖 `pflag.Parse()`。
2. 远程 watch 必须 `injectRemote`（Reset 底层）后**重新叠加缓存的 `localMap`**，否则本地会被清掉。
3. 本地 watch 的 `OnConfigChange` 回调内要重新 `parseLocal + MergeConfigMap`，否则文件变更不生效（主 `v` 从未 `ReadInConfig`，`ConfigFileUsed()` 需提前 `SetConfigFile` 才能定位文件）。

**一句话经验**：viper 里「覆盖远程」不必进 override 层——让远程与本地同处底层、**本地后写赢**即可；真正的硬优先级（flag / env）交给 viper 上层。这样既能「本地覆盖远程」，又能「flag 压本地」，且 watch 各自只动底层、互不污染。

---

## 5. 多环境设计

> ✅ 已拍板：**路线 1（env 进入 path/namespace）**。

- **本地**：`Options.Env` 非空时按 `{Name}-{Env}.yaml/json` 选择默认文件（如 `bald-demo-prod.yaml`）；
  也可经 `appkit.Env("prod")` 在 AppKit 层设置。
- **远程**：`--env` 由用户在构造后端 path 时拼接（如 `/config/demo/prod.yaml`），Load 不感知远程 path；
  鉴权/TLS 等凭据由后端构造时持有（`RemoteSource` 接口不携带，避免过度耦合）。

| 路线 | 做法 | 优点 | 缺点 |
|---|---|---|---|
| **路线 1：env 进入 path/namespace** ✅ | `--env=prod` 拼到远程路径/namespace，如 `/config/demo/prod.yaml`；本地按 `config-prod.yaml` 选择 | 环境隔离清晰、远程一套存储多环境 | 需约定路径规范 |
| 路线 2：env 仅选 source | `--env` 只决定用哪套 Sources（如 prod 用远程、dev 用本地）| 实现简单 | 环境切换不灵活 |

---

## 6. 热更新

- **本地**：保留 viper `WatchConfig`（fsnotify），变更触发 `OnConfigChange`。
- **远程**：`RemoteSource.Watch` 回调拿到新字节 → `injectRemote` 重新注入 viper → 手动触发 `OnConfigChange(v)`。
- 业务在 `OnConfigChange` 内重新 `v.Unmarshal(&opts)` 完成热重载（后续可进一步 hook 到 AppKit 内部 options 重载）。

---

## 7. 决策记录（已拍板，2026-08-18）

| # | 决策点 | 结论 |
|---|---|---|
| 1 | 本地 vs 远程优先级 | ✅ **远程基准 + 本地覆盖**（远程与本地同处底层 config、本地后写赢，flag/env 在更上层压本地，watch 互不污染，详见 4.1/4.2）|
| 2 | 多环境路线 | ✅ **路线 1**（env 进入 path/namespace；本地按 `Name-Env.yaml`，远程 path 用户构造时拼接）|
| 3 | 远程后端范围 | ✅ **接入 Kratos 实现**：通过 `config.FromKratosSource(kratosconfig.Source)` 桥接 kratos contrib 的 etcd/consul/nacos/apollo 等后端，**不重复造轮子**（与 `registry.FromKratos` 思路一致）；bald 仅依赖 kratos 核心 config 接口 |
| 4 | 热更新回调粒度 | ✅ **裸 viper**（`OnConfigChange(*viper.Viper)`），业务自 Unmarshal |
| 5 | 鉴权字段 | ✅ **预留**：鉴权/TLS 由后端构造时持有，`RemoteSource` 接口不携带，避免过度耦合 |

---

## 8. 与现有 `pkg/config` 的关系

- 保留：viper 作为内存配置中心、`OnConfigChange` 回调、`ConfigFile` / `WatchConfigFile` / flag / env 解析。
- 替换：已删除 `RemoteProvider{AddRemoteProvider/ReadRemoteConfig}` 的 viper remote 用法，
  改为 `RemoteSource` 接口 + `injectRemote` 手动注入（见 `pkg/config/source.go`）。
- 兼容：对外 Option 形态尽量稳定（`RemoteConfig(src RemoteSource)` 取代旧 `RemoteConfig(rp *RemoteProvider)`）。

---

## 9. 参考实现

| 框架 | 方案 | 借鉴点 |
|---|---|---|
| **Kratos** | `Source`/`Watcher` 接口 + `Reader` 深度合并；后端自带 `Format`；watch 由各 SDK 保证 | Source 抽象、格式自声明、多源合并 |
| **go-lulu** | `Reader`/`ValueWatcher`/`Decoder` 正交拆分；etcd `WatchValue` 直接推新字节 | 推值模式（省一次重新 Get）、连接探活 |
| **viper（仅本地）** | `SetConfigFile`/`ReadInConfig`/`WatchConfig` | 本地加载与热更新壳 |

---

## 10. 可运行示例

完整的「本地 + 远程 + 多环境 + 热更新」可运行示例见 [`cmd/bald/main.go`](cmd/bald/main.go)：

- 通过 `appkit.ConfigFile` / `appkit.Env` / `appkit.WatchConfigFile` / `appkit.RemoteConfig` / `appkit.OnConfigChange` 演示了本文全部决策点的落地方式；
- 远程后端给出 **etcd** 与 **nacos** 两种 `config.FromKratosSource` 桥接写法（含 `go get` 提示）；
- 本地配置样例见 [`configs/bald-demo.yaml`](configs/bald-demo.yaml)，并说明远程同名 key 作基准、本地覆盖的语义。

