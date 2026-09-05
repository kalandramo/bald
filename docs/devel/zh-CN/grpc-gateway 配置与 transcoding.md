# grpc-gateway 配置与 transcoding

本文给出 bald 接入 **grpc-gateway** 的完整示例：用一份 proto 同时定义 gRPC 接口与 REST/JSON 映射（transcoding），业务只实现一次，gin 与 grpc-gateway 复用同一 biz 层。

> 前置：本仓库**不内置 protoc 工具链与 grpc-gateway 依赖**。`_example/bald` 下的示例代码用 `//go:build grpcgw` build tag 保护，默认 `go build` 不编译它。请在本机安装工具链后生成代码并启用（见下文）。

---

## 1. 目录结构

```
_example/bald/
├── main.go                 # 示例入口（bootstrap 装配 + HTTP + gRPC + AppKit 编排；gatewayFactory 默认 nil）
├── register_grpcgw.go      # build tag `grpcgw`：init 注入 gRPC/gateway 接线 + biz 实现
├── proto/
│   ├── greet.proto         # 真实 proto + google.api.http 注解
│   ├── buf.yaml            # buf 模块配置（依赖 googleapis）
│   └── buf.gen.yaml        # 生成器配置（go / go-grpc / grpc-gateway）
├── Taskfile.yml            # `task proto` 触发 buf generate（跨平台，Windows 亦可用）
└── gen/                    # buf generate 产物（.pb.go / _grpc.pb.go / _pb.gw.go；
                             #   包名 baldv1，paths=source_relative 产物落在 gen/ 根目录）
```

---

## 2. proto 定义（含 transcoding）

`proto/greet.proto` 关键片段：

```proto
service GreetService {
  rpc Greet(GreetRequest) returns (GreetResponse) {
    option (google.api.http) = {
      post: "/v1/greet"
      body: "*"
    };
  }
  rpc GreetGet(GreetGetRequest) returns (GreetResponse) {
    option (google.api.http) = {
      get: "/v1/greet/{name}"
    };
  }
}
```

`google.api.http` 注解即 transcoding 规则：同一个 RPC 同时暴露为
- `POST /v1/greet`（JSON body）
- `GET /v1/greet/{name}`（路径变量）

无需业务写两遍 handler。

---

## 3. 生成代码

安装工具链（一次性）：

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@latest
# buf：见 https://buf.build/docs/installation
```

生成：

```bash
cd _example/bald
task proto        # buf generate proto --template proto/buf.gen.yaml
go mod tidy       # 首次会把 grpc-gateway/v2 等写入 go.mod
```

产物 `gen/`（Go 包名 `baldv1`，导入路径 `github.com/kalandramo/bald/example/bald/gen`）：
- `greet.pb.go`：message 与 `GreetServiceServer` 接口
- `greet_grpc.pb.go`：`RegisterGreetServiceServer` / `ServiceDesc`
- `greet.pb.gw.go`：`RegisterGreetServiceHandler(ctx, mux, conn)`

---

## 4. 接线到 bald server（框架契约）

transport module（2026-09-05 自 pkg/server 迁入）提供两个载体：

- **gRPC**（子包 `transport/grpc`，包名 `grpcserver`）：
  `grpcserver.NewGRPCServerWithRegister(cfg *bootstrapv1.Server_Grpc, unary []grpc.ServerOption, register func(s *grpc.Server), readiness transport.ReadinessFunc)` —— 包装新建的 `*grpc.Server`，自动注册 health/reflection，`register` 回调里挂业务 service。
- **gateway**（子包 `transport/gateway`，包名 `gateway`）：
  `gateway.NewGatewayServer(httpCfg *bootstrapv1.Server_Http, grpcBackend *bootstrapv1.Server_Grpc, register func(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error), readiness transport.ReadinessFunc)` —— `register` 回调**自建** `runtime.ServeMux` 并作为 `http.Handler` 返回（`transport` 因此不必依赖 grpc-gateway）。

  其中 `grpcBackend` 是 `*bootstrapv1.Server_Grpc` 指针（**不是**固化字符串）：网关在 `Start`
  时才读 `grpcBackend.GetAddr()`，因此 env/flag 对 `grpc.addr` 的覆盖能正确生效；
  若传构造期快照字符串，会锁死默认端口（如 `:9090`），导致测试用 `t.Setenv`
  覆盖地址无效。

`register_grpcgw.go`（build tag `grpcgw`）即此接线示例：

```go
// biz 层：被 gin（web.HandleJSONRequest）与 gRPC（GreetService）共用。
func Greet(ctx context.Context, name string) (string, error) {
    if name == "" {
        name = "world"
    }
    return "hello, " + name, nil
}

// 必须嵌入 UnimplementedGreetServiceServer（protoc-gen-go-grpc 默认
// require_unimplemented_servers，否则编译报 mustEmbedUnimplemented...）。
type greetService struct {
    baldv1.UnimplementedGreetServiceServer
}

func (s *greetService) Greet(ctx context.Context, req *baldv1.GreetRequest) (*baldv1.GreetResponse, error) {
    greet, err := Greet(ctx, req.GetName())
    if err != nil {
        return nil, err
    }
    return &baldv1.GreetResponse{Greet: greet}, nil
}

// 传给 grpcserver.NewGRPCServerWithRegister 的 register 回调
func registerGRPCServiceFn(s *grpc.Server) {
    baldv1.RegisterGreetServiceServer(s, &greetService{})
}

// 传给 gateway.NewGatewayServer 的 register 回调：
// 自建 grpc-gateway v2 的 runtime.ServeMux（非标准库 http.ServeMux），
// 注册 handler 后作为 http.Handler 交回 GatewayServer。
func registerGateway(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error) {
    mux := runtime.NewServeMux()
    if err := baldv1.RegisterGreetServiceHandler(ctx, mux, conn); err != nil {
        return nil, err
    }
    return mux, nil
}
```

`GatewayServer` 内部用 `grpc.NewClient` 连到 `grpcBackend.GetAddr()`（本进程 gRPC 端口），把 REST 请求反向代理成 gRPC 调用——这正是 transcoding 的运行时形态。

---

## 5. 启用示例

main.go 默认以空实现接线（`registerGRPCService` 空回调、`gatewayFactory == nil` 不挂网关），保证无 protoc 环境下 `go build ./...` 可编译。启用完整 transcoding：

1. 完成第 3 步生成代码（`gen/*.go` 存在）。
2. 用 `-tags grpcgw` 编译运行——`register_grpcgw.go` 的 `init()` 会把真实的
   `registerGRPCServiceFn` 注入 `registerGRPCService`、把 `newGatewayWithGreet` 注入
   `gatewayFactory`，`buildServers` 即挂上网关服务器（独立 `gatewayAddr`，默认 `:8081`，
   与 HTTP 主服务 `http.addr` 端口错开）：

```bash
go run -tags grpcgw ./_example/bald            # 配置随示例自带，自动加载
```

---

## 6. 验证 transcoding

```bash
# gRPC（需 grpcurl / 客户端）
grpcurl -plaintext -d '{"name":"bald"}' localhost:9090 bald.v1.GreetService/Greet

# REST / JSON（transcoding，由 gateway 暴露）
curl -i -XPOST http://127.0.0.1:<gateway-addr>/v1/greet -H "Content-Type: application/json" -d '{"name":"bald"}'
# => {"greet":"hello, bald"}

curl -i http://127.0.0.1:<gateway-addr>/v1/greet/bald
# => {"greet":"hello, bald"}
```

同一份 biz 函数 `Greet`，既服务于 gin 的 `web.HandleJSONRequest`（HTTP 直连），又服务于 grpc-gateway transcoding（REST→gRPC），印证 **gin 与 grpc-gateway 复用同一 biz 层 Handler**。

---

## 7. 约定

1. **transcoding 规则写在 proto 的 `google.api.http` 注解里**，不要手写 REST handler。
2. **biz 函数与传输层解耦**：gRPC 实现层只做"取字段 → 调 biz → 组装响应"的薄适配。
3. **gateway 与 gRPC 同进程**：`GatewayServer` 实时读 `grpcBackend.GetAddr()` 连本进程 gRPC，无需独立部署。
4. **启用需 build tag**：示例接线文件用 `//go:build grpcgw` 保护，避免无 protoc 环境编译失败。
