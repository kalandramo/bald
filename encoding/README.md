# Encoding

统一的编解码抽象层，为 bald 的所有传输面（HTTP / gRPC / TCP / WebSocket / SSE 等）提供可拔插的序列化能力。

通过 `encoding.Codec` 接口定义统一的 Marshal/Unmarshal 契约，支持 12 种编解码格式；传输层通过名称（如 `WithCodec("json")`）取用编解码器，切换格式无需修改业务代码。

## 核心设计

- **接口驱动**：所有编解码器实现 `Codec` 接口（`Marshal` / `Unmarshal` / `Name`）
- **显式注册**：框架原则不用 `init()` + blank import 自注册——各格式包导出 `New()` 构造器，应用在装配期显式 `encoding.MustRegister(...)`；nil / 空名 / 重名一律 panic（fail-fast）
- **按需引入**：每种格式是独立 module，只 require 用到的格式，未引入的格式不进二进制、不进依赖图
- **大小写无关**：编解码器名称大小写无关，`"json"` / `"JSON"` / `"Json"` 均可

## 模块布局

```
bald/encoding/          # 根包：Codec 契约 + 全局注册表（零三方依赖，独立 module）
bald/encoding/json/     # 各格式独立 module，require 根 module + replace => ..
bald/encoding/proto/
...
```

## Codec 接口

```go
type Codec interface {
    Marshal(v any) ([]byte, error)
    Unmarshal(data []byte, v any) error
    Name() string
}
```

## 快速开始

### 显式注册与使用

```go
import (
    "github.com/kalandramo/bald/encoding"
    jsoncodec "github.com/kalandramo/bald/encoding/json"
)

func main() {
    encoding.MustRegister(jsoncodec.New())

    codec := encoding.GetCodec("json")

    // 编码
    data, err := codec.Marshal(map[string]string{"hello": "world"})

    // 解码
    var result map[string]string
    err = codec.Unmarshal(data, &result)
}
```

### 注册自定义编解码器

```go
import "github.com/kalandramo/bald/encoding"

type myCodec struct{}

func (myCodec) Marshal(v any) ([]byte, error) { /* ... */ }
func (myCodec) Unmarshal(data []byte, v any) error { /* ... */ }
func (myCodec) Name() string { return "custom" }

// 装配期显式注册（不用 init()）
encoding.MustRegister(myCodec{})
```

## API 参考

| 函数 | 说明 |
|------|------|
| `MustRegister(c Codec)` | 显式注册编解码器；nil/空名/重名 panic（fail-fast），名称大小写无关 |
| `GetCodec(name string) Codec` | 按名称获取编解码器，未注册返回 `nil`（调用方 fail-fast） |
| `Names() []string` | 全部已注册名称（升序），用于诊断 |

## 支持的编解码格式

### 文本格式

| 名称 | 包路径 | 说明 |
|------|--------|------|
| `json` | `encoding/json` | JSON（标准库），通用性最强 |
| `xml` | `encoding/xml` | XML（标准库），SOAP / 传统系统 |
| `yaml` | `encoding/yaml` | YAML（gopkg.in/yaml.v3），配置文件 |
| `toml` | `encoding/toml` | TOML（BurntSushi/toml），配置文件 |

### 二进制格式

| 名称 | 包路径 | 说明 |
|------|--------|------|
| `proto` | `encoding/proto` | Protocol Buffers，高性能 RPC |
| `msgpack` | `encoding/msgpack` | MessagePack（vmihailenco/msgpack/v5），紧凑二进制 |
| `bson` | `encoding/bson` | BSON（mongo-driver），MongoDB 原生格式 |
| `cbor` | `encoding/cbor` | CBOR（fxamacker/cbor/v2），RFC 8949，WebAuthn / COSE |
| `gob` | `encoding/gob` | Go Gob（标准库），Go-to-Go 内部通信 |
| `thrift` | `encoding/thrift` | Apache Thrift 二进制协议，需 TStruct 生成代码 |

### Schema 驱动格式

| 名称 | 包路径 | 说明 |
|------|--------|------|
| `avro` | `encoding/avro` | Apache Avro（linkedin/goavro/v2），大数据 / Kafka |
| `flatbuffers` | `encoding/flatbuffers` | Google FlatBuffers，零拷贝序列化 |

## 特殊格式说明

### Avro（Schema 驱动）

`New()` 返回的默认编解码器使用空 schema，仅支持原始类型。复杂记录类型需通过 `NewCodec` 创建：

```go
import "github.com/kalandramo/bald/encoding/avro"

schema := `{"type":"record","name":"User","fields":[{"name":"name","type":"string"}]}`

codec, err := avro.NewCodec(schema)
if err != nil {
    panic(err)
}

data, err := codec.Marshal(map[string]any{"name": "Alice"})

var result map[string]any
err = codec.Unmarshal(data, &result)
```

### Protobuf

`Marshal` / `Unmarshal` 的参数必须实现 `proto.Message` 接口（即通过 protoc 生成的结构体）。

### Thrift

参数必须实现 `thrift.TStruct` 接口（即通过 thrift 编译器生成的结构体）。

### FlatBuffers（零拷贝）

`Marshal` 需要值实现 `FlatBufferMarshaler` 接口，`Unmarshal` 需要目标实现 `flatbuffers.FlatBuffer` 接口。两者均由 `flatc` 编译器生成的代码提供。

## 设计原则

- **禁止手写编解码逻辑**：所有序列化/反序列化必须通过 `encoding.Codec` 进行
- **统一可拔插**：传输层按名称取用编解码器，一行切换格式，无需修改业务逻辑
- **显式注册 fail-fast**：重复注册、未注册取用都在装配/启动期暴露，不静默覆盖
- **按需引入**：每种格式独立 module，只引入需要的编解码格式
- **零侵入**：编解码器与传输层解耦，同一份业务代码适配所有格式
