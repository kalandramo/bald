# Bald 错误模型设计

> Title: berrors——传输中立的错误模型：零依赖核心 + 对等传输桥接子包
>
> Author(s): bald 团队
>
> Last updated: 2026-09-15
>
> Status: Accepted（对应实现 `berrors` module，tag `berrors/v0.1.0`；本文并入并取代原《错误模型设计》，修正其两处与实现漂移的表述——模块归属与栈捕获时机）

## 摘要

berrors 是 bald 的错误契约 module：一个模型、三个包。根包定义传输中立的
`Error`（`Code` + 稳定 `Reason` + 用户 `Message` + i18n `Details` + 可选
`cause` 与调用栈）、不可变 builder、按 `Reason` 匹配的 `Is`；gRPC 双向转换
放 `grpcerr` 子包，HTTP 状态码映射放 `httperr` 子包。整个 module 只有一个
核心承诺：**根包 import 级零依赖——纯 HTTP 服务、CLI 工具、单元测试都能
import `berrors` 而不链接任何传输框架**。为此我们放弃了「把 `GRPCStatus()`
挂在核心类型上」的便利，让桥接成本精确落到使用对应传输的项目。

本文是 berrors module 的完整专文，吸收原《错误模型设计》的全部决策（①~⑧）
并新增决策⑨（2026-09-15 结构复审：桥接子包不并入根包）。错误模型是依赖图
第 0 层叶子（见《[框架契约总览](框架契约总览.md)》§0「模块与依赖层级」）；HTTP 边界消费方式见
《[路由注册与绑定设计](路由注册与绑定设计.md)》，前端错误解析见《[示例前后
端契约同步设计](示例前后端契约同步设计.md)》，API 速查见《[框架契约总览]
(框架契约总览.md)》§7。

## 背景与动机

### 两个参考实现各有一半正确

go-lulu `WindError` 的骨架是对的——零依赖 + 不可变 builder 让 sentinel 派生
安全：

```go
var ErrOrderNotFound = errors.NotFound("ORDER_NOT_FOUND")
return ErrOrderNotFound.WithDetails(map[string]string{"id": id}) // 新实例，原变量不变
```

但它缺两块：没有面向用户的 `Message`；gRPC 方向半通——发送要手写
`codes.Code(wErr.Code)`，接收端无法反向解析，跨服务语义丢失。

onexstack `ErrorX` 补上了这两块（`Message` 字段、`GRPCStatus()` 自动附
`errdetails.ErrorInfo`、`FromError` 反向解析），但把核心包绑死 Kratos + gRPC，
`Code` 直接是 HTTP 状态码，且 builder 可变——sentinel 被就地污染是真实
bug 源：

```go
// onexstack：就地修改，sentinel 被污染，后续所有调用方共享上一次的 id。
return ErrOrderNotFound.WithMetadata(map[string]string{"id": id})
```

一句话定性：bald 需要「go-lulu 的骨头 + errorsx 的血肉」——零依赖、不可变、
带 Message、gRPC 双向闭环，且不能让根包背上任何传输依赖。

### 两次身份变化

初版落地为根模块 `pkg/berrors`、包名 `errors`。两个现实问题相继出现：

1. **包名遮蔽代价大于收益**（2026-09-11 改名 `berrors`）：遮蔽标准库后全仓
   40+ 处被迫写别名 import；改名后包名与目录/模块一致，别名消失，标准库
   可同文件共存。
2. **发布粒度**（2026-09-11 独立 module）：错误契约是 transport（第 1 层）
   等多个模块的共同依赖，留在根模块会拖整个根模块闭包。独立后成为第 0
   层叶子（与 bconf/log/bconfig 互不依赖），发布 `berrors/v0.1.0`。

### 2026-09-15 结构复审

用户提问「grpcerr/httperr 能否并入根包」。评估：技术可行（三包同属一个
module、API 无冲突、全仓仅 6 处 import），但被否决——决策⑨，见「理由与
取舍」。

## 设计

### 总览：一个模型、三个包、一条依赖方向

| 包 | 职责 | 第三方依赖 |
|---|---|---|
| `berrors`（根包） | `Error` 类型、`Code` 常量与 11 工厂、不可变 builder、`Is`/桥接 | **零**（纯标准库） |
| `grpcerr` | gRPC 双向转换 + `errdetails.ErrorInfo` | grpc + genproto |
| `httperr` | HTTP 状态码投影（两张表三函数） | **零**（int 状态码） |

一个需要精确表述的细节：**module 的 go.mod require 了 grpc v1.83.2 与
genproto**——那是 grpcerr 与根包同 module 的自然结果。零依赖承诺保护的不是
go.sum（require 一直在），而是**链接产物**：消费方不 import `grpcerr`，二
进制里就不出现 grpc；`import berrors` 永远只拉标准库。

零依赖承诺撑得起这道承重墙，是因为错误的一生只有三段，前两段占业务代码的
绝大多数、且只需要根包：

```
产生（源头）──→ 匹配（消费）──→ 投影（边界）
   根包             根包            grpcerr / httperr
```

biz 构造（`berrors.NotFound("user/not_found")`——错误诞生时不知道、也不该
知道将来以何种传输形态出去）、补偿与降级分支的 `Is` 匹配、DAL 层
`WithCause` 包装、CLI 与单元测试的 sentinel 断言，全部只 import 根包；投影
段被框架收口（HTTP 侧 `web.ErrorResponse`、gRPC 侧 `ErrorInterceptor`），
业务代码几乎不碰桥接子包。实证见「实现与过渡」消费面：两仓合计 41 个文件
只 import 根包，桥接子包的 6 处 import 全部是框架收口点或测试。

### Error：四个公开字段 + 两个附加物

```go
type Error struct {
    Code    uint32            `json:"-"`       // 传输类别，与 gRPC codes.Code 1:1
    Reason  string            `json:"reason"`  // 领域稳定标识，Is 按它匹配
    Message string            `json:"message"` // 可展示给最终用户的简短文案
    Details map[string]string `json:"details"` // i18n 动态变量
    cause   error             `json:"-"`       // error 链
    stack   []uintptr         `json:"-"`       // 失败点调用栈
}
```

边界：`Code` 取值与 gRPC 协议 1:1（17 常量），**数值永不改动**——与 gRPC
协议、httperr 映射表、`ErrorInfo` 语义一一对应。`Reason` 是不可变契约：永不
本地化，决定 `Is` 匹配。`Message` 是完整一句话，`Details` 是 i18n 模板变量，
两者都写不冲突。

### 构造：工厂优先；栈在构造点捕获

```go
return berrors.NotFound("ORDER_NOT_FOUND")                          // 工厂优先
return berrors.NotFound("ORDER_NOT_FOUND").WithMessage("订单 %s 不存在", id)
```

11 个工厂覆盖最常用传输类别；`New(code, reason)` 只留给工厂之外的场景。
**栈捕获发生在 `New` 构造点**（`errors.go`：「构造即捕获失败点调用栈（对齐
go-lulu WindError），无需等 With* 才记录」）——原《错误模型设计》写的「延迟
到首个 With*」是落地前草案，与实现相反，本文修正。`With*` 上的
`captureStack` 是幂等保护，不覆盖构造点栈。

### 不可变 builder：sentinel 永远安全

```go
func (e *Error) WithMessage(format string, args ...any) *Error
func (e *Error) WithDetails(details map[string]string) *Error
func (e *Error) WithCause(cause error) *Error
func (e *Error) WithCode(code uint32) *Error   // 逃生舱：覆盖传输类别
```

所有 `With*` 返回新实例，接收者绝不被修改。`Details` 是 `Error` 上**唯一的
引用类型字段**（`Code`/`Reason`/`Message` 均为值类型，拷贝天然隔离），故
`clone` 与 `WithDetails` 都对它**深拷贝**——2026-09-18 修复：此前浅拷贝使派生
实例与源实例共享同一 map，派生实例就地改 `Details` 即污染 sentinel，击穿
「sentinel 永远安全」的承诺。`nil` 保持 `nil`（大多数错误无 Details，零分配）。

### 匹配与标准库桥接

`Is` 按 `Reason` 相等匹配，`Code/Message/Details/cause` 故意忽略——`Code`
是易变传输分类（同一失败在不同服务可能是 `NotFound` 或 `Internal`），
`Reason` 才是稳定契约。根包重导出 `Is/As/Unwrap/Join` 桥接（包名已不遮蔽
标准库，桥接是纯便利）；`FromError(err)` 从任意错误链提取 `*Error`。

### httperr：Code 的 HTTP 投影

- **正向 17 码全表**（对齐 grpc-httpjson-transcoding / Envoy 规范）：
  `NotFound→404`、`ResourceExhausted→429`、`Canceled→499`（nginx 约定）、
  `Unavailable→503` 等；未知码兜底 500，绝不产生误导性 2xx。
- **反向 11 码**：正向是多对一（`FailedPrecondition`/`OutOfRange` 都落
  400），反向一码一义取最贴切者；未知状态兜底 `CodeUnknown`。

```go
func CodeToHTTP(code uint32) int
func HTTPToCode(httpStatus int) uint32
func StatusCode(e *berrors.Error) int   // CodeToHTTP(e.Code)
```

刻意用 `int` 而非 `http.StatusCode`；`StatusCode` 刻意是函数而非方法——挂
在类型上会让根包反向 import `httperr`（循环依赖）。

### grpcerr：gRPC 双向闭环与 Kratos 互通

```go
func ToStatus(err error) *status.Status   // + errdetails.ErrorInfo{Reason, Metadata}
func FromStatus(st *status.Status) error  // 从 ErrorInfo 恢复 Reason/Details
```

两条诚实声明的边界：
- 其一，`FromStatus` 对无 `ErrorInfo` 的普通 status：`Code` 与 `Message` 从
status 取，`Reason` **留空**——刻意不把可变的 status 文本塞进 `Reason`，
因为 `Reason` 是 `Is` 的匹配键、必须稳定（2026-09-18 修复：此前把文本塞进
`Reason`，导致两个同类失败因文本不同而无法互相 `Is` 匹配）。带 `ErrorInfo`
时 `Reason`/`Details` 完整还原。
- 其二，Kratos 互通是天然的：两边都产/认 `ErrorInfo`，`FromStatus` 能解析 Kratos 错误，`ToStatus` 的
产出也能被 Kratos 客户端理解——不依赖 Kratos 包。

集成纪律（`pkg/middleware/grpc/error.go`）：`ErrorInterceptor` 必须放在拦截
器链**最外层**，内层（校验、认证）的错误才能全部被收口；否则 gRPC 框架的
`status.Convert` 会把 `*berrors.Error` 当普通 error，只剩 `Error()` 文本进
`Unknown`，Code/Reason/Details 全丢。

### 错误响应体：google.rpc.Status JSON，三面一份契约

错误出三面（gRPC / gateway 转码 / gin 直连）曾不同形：gin 面的
`{"error": err.Error()}` 把日志串当展示文案，`Message` 从未到达前端。定稿
形状（决策⑧，`transport/web` 的 `StatusBody`/`StatusOf`/`ErrorResponse`
契约）：

```json
{
  "code": 5,
  "message": "订单 42 不存在",
  "details": [ { "@type": "type.googleapis.com/google.rpc.ErrorInfo",
                 "reason": "ORDER_NOT_FOUND", "domain": "", "metadata": {"id": "42"} } ]
}
```

与 gateway 转码输出**字节级同形**（实测验证）：空值语义对齐 protojson
（`""`/`{}`/`[]`），非 berrors 错误兜底 `INTERNAL`。前端只写一次解析：展示读
`message`，程序化读 `details[0].reason`。

## 理由与取舍

**决策①：根包零依赖、桥接拆子包——承重墙。** 纯 HTTP 服务、CLI（`cmd/bald`
自己就是）、单元测试都不该为一个错误类型背上 gRPC。被放弃：把
`GRPCStatus()` 挂在核心类型上（errorsx 做法）——省 20 行，根包从此依赖
grpc + genproto，不用 gRPC 的调用方也被迫链接。

**决策②：`Code` 是传输中立 `uint32`，不是 HTTP 码。** 一个错误要能以 HTTP
形态出去、以 gRPC 形态进来；HTTP 码只是 `Code` 的一次投影。被放弃：直接用
`codes.Code` 类型——省一次转换，但根包立刻依赖 gRPC。

**决策③：不可变 builder。** 可变 builder 的 sentinel 污染是真实 bug 源（跨
请求串数据）。代价是每次派生一次小分配——错误构造不是热路径。

**决策④：`Is` 只比 `Reason`。** errorsx 比 `Code + Reason` 混入易变维度，
跨服务不可靠。被放弃：比全部字段——`Details` 每次派生都不同，sentinel
匹配直接失效。

**决策⑤：`Message` 与 `Details` 并存。** 两个消费方两种载体：前者给用户看
完整一句话，后者给前端做 i18n 模板。只留一个都有缺口。

**决策⑥：不照搬 go-lulu 整包、也不包 wrapper。** vendor 它等于把两个缺口
（Message、gRPC 反向解析）也搬进来；wrapper 破坏 `errors.Is` 链与
`ErrorInfo` 透传。

**决策⑦：错误模型独立成包，而非路由文档的 `web.Error`。** 错误是跨协议概念
（HTTP + gRPC + job + CLI），不该属于 web 层。路由文档 §4 提案由本设计取代
（该提案未落地过代码，零迁移成本）。

**决策⑧：HTTP 错误响应体 = google.rpc.Status JSON。** 三面一份契约，细节见
「设计」末节。被放弃：Kratos 信封 `{code, data, message}` 一切 200——破坏
proto3 JSON 契约（TS 生成类型需解包层）；HTTP 语义丢失（浏览器/网关/监控
看不到真实状态码）；httperr 双向映射表整个失效。

**决策⑨（2026-09-15）：桥接子包不并入根包。** 挑战来自合理直觉：三包同属
一个 module，合并似乎更简洁。否决理由：其一，grpcerr 并入 = 决策①弃案重演
——module 本就 require grpc，合并不改 go.sum，改变的是**链接产物**：
`import berrors` 即链接 grpc 全家桶，纯 HTTP 服务与 CLI 首先受害；其二，
grpcerr/httperr 是文档明写的「对等桥接子包」，只并 httperr 打破对称，全并
回到第一条；其三，收益仅省一个包前缀（全仓 6 处 import），与代价严重不对
称。被放弃：a) 全并入；b) 仅并入 httperr；c) 反向拆 grpcerr 独立子模块让
module 回归零第三方 require——下游几乎必有 grpc，收益有限而多一个子模块
的发布维护成本。

## 兼容性

**落地以来只有一次破坏性变更，且已完成。** 包名 `errors`→`berrors`
（2026-09-11）：全仓 40+ 处别名 import 消失。此后 API 面纯增量。

**已知代价（诚实列出）：**

- 用 gRPC 的项目必须显式 import `grpcerr` 并在拦截器/客户端各写一行转换
  ——比 errorsx「开箱即用」多 2 行样板，决策①的明码标价。
- web 层取状态码是 `httperr.StatusCode(e)` 而非 `e.StatusCode()`——多一次
  包前缀，决策①在 HTTP 侧的对称延伸。
- `Error()` 输出（`code/reason/cause`）是日志取向，`Message` 刻意不混入
  ——需要展示时取字段而非字符串。
- 每次 `With*` 一次 `clone` 分配、`New` 一次 `runtime.Callers`——非热路径。
- module go.mod require grpc：不 import `grpcerr` 的消费方不受影响（链接
  隔离），但 go.sum 体积可见。反向拆分被决策⑨否决。

## 实现与过渡

**全部已落地**，收敛为 4 个源文件 + 3 个测试文件（**20 例单测**；2026-09-18 评审补 8 例回归。此前本文写「13 例」，实测为 12，为笔误）：

| 文件 | 内容 | 测试要点 |
|---|---|---|
| `errors.go` | `Error` 全套（类型/构造/builder/匹配/桥接/栈） | sentinel 不可污染、Is 三态、FromError 三态、栈含调用帧 |
| `code.go` | 17 常量 + 11 工厂 | 经工厂间接覆盖 |
| `httperr/httperr.go` | 两张映射表 + 三函数 | 典型映射、未知双向兜底 |
| `grpcerr/grpcerr.go` | `ToStatus`/`FromStatus` | RoundTrip 含 Is 跨栈命中、Kratos 风格 ErrorInfo 解析 |

**消费面（全仓 6 处 import）：** `transport/web`（决策⑧响应体契约）、
`pkg/middleware/grpc`（ErrorInterceptor 收口）、`pkg/middleware/gin`
（authn/authz 取 401/403）、`_example/bald`（e2e 验证透传）——全部是投影
收口点或测试。对照面（2026-09-15 统计）：bald 内只 import 根包的消费文件
16 个；下游 bald-admin 后端 25 个（biz 八域 + gin/grpc handler），桥接子包
0 处。投影被框架收口后业务仓库与桥接天然解耦——这就是决策①零依赖承诺
的兑现面。

### 2026-09-18 评审修复（四处）

一次架构评审（天权）用探针实测发现四处「设计承诺 vs 实现」的不一致，全部修复并补回归测试。每条都先写 RED（断言「正确行为」）、修复后 GREEN，并做回滚验证（还原修复后测试复红）：

| # | 严重度 | 缺陷 | 修复 | 回归测试 |
|---|---|---|---|---|
| 1 | 高 | gRPC roundtrip 丢 `Message`——`ToStatus` 把它放进 status，`FromStatus` 却从未赋回字段 | `FromStatus` 显式 `ret.Message = st.Message()` | `TestRoundTripPreservesMessage` |
| 2 | 中 | 无 `ErrorInfo` 的普通 status 把可变文本塞进 `Reason`，污染 `Is` 匹配键 | `Reason` 留空，文本归位 `Message` | `TestPlainStatusKeepsReasonClean` |
| 3 | 中 | `clone` 浅拷贝 + 导出 `Details` → 派生实例就地改 map 污染 sentinel | `clone`/`WithDetails` 深拷贝（`nil` 保持 `nil`） | `TestImmutableBuilder_DetailsNotShared`、`TestWithDetails_CopiesInputMap` |
| 4 | 低 | `CodeToHTTP(Canceled)=499` 有正向，`HTTPToCode(499)` 反向缺失兜底 `Unknown` | 反向表补 `499: CodeCanceled` | `TestHTTPMappingSymmetry`、`TestCanceledRoundTrip` |

**评审指出的两个测试盲区**（缺陷恰好都落在没断言的地方）：原 `TestToStatusAndFromStatusRoundTrip` 只断言 `Reason`/`Details` 漏了 `Message`；`httperr` 测试没做正反向对称性断言。两者均已补。

**未改的边界（评审确认非缺陷）**：`WithCode` 是唯一不调 `captureStack` 的 `With*`，但它经 `clone` 复用 `New` 构造点的栈，行为正确；`sres` 式「构造签名是否统一」等属其他 module 议题，不在本 module 范围。

## 附录：FAQ

**为什么不把 grpcerr/httperr 并进根包？** 决策⑨：技术上可行，但
`import berrors` 会链接 grpc 全家桶，零依赖承诺作废。省一个包前缀不值。

**和 Kratos `api/errors` 是什么关系？** 平行实现，经 `grpcerr` 互转；不依
赖它——自带 gRPC 依赖，违反决策①。

**栈是构造时捕获还是 With* 时捕获？** 构造时（`New` 即 `captureStack`）。
原《错误模型设计》写反了这一条，本文已修正。

**`Message` 进不进 `Error()`？** 不进。`Error()` 给日志，`Message` 给用户
（字段直接取）；混入会污染日志且无法结构化消费。
