# grpc-gateway 配置与 transcoding

本文给出 bald 接入 **grpc-gateway** 的完整示例：用一份 proto 同时定义 gRPC 接口与 REST/JSON 映射（transcoding），业务只实现一次。网关不是独立服务器：转码面由 `transport/gateway/contract` 的 `HTTPProvider` 承载，它接管契约 `server.http` 段——HTTP 端口即转码面（gin 面让位），`driver` 留空或写 `grpc-gateway` 都准入。

> 前置：本仓库**不内置 protoc 工具链与 grpc-gateway 依赖**。`_example/bald` 下的示例代码用 `//go:build grpcgw` build tag 保护，默认 `go build` 不编译它。请在本机安装工具链后生成代码并启用（见下文）。

---

## 1. 目录结构

```
_example/bald/                     # 生成树（appspec.json 是声明真相源）
├── appspec.json                   # roles / bootstrap / domains / variants
├── cmd/bald-demo-api/app/         # command.go（生成）+ custom.go（seeded）
├── cmd/bald-demo-worker/app/
├── internal/bootstrap/
│   ├── assemble.gen.go            # gen app 投影：Options(role)
│   ├── variants.gen.go            # 变体声明（恒进计划）
│   ├── variant_grpcgw.gen.go      # //go:build grpcgw 的实现注册
│   ├── custom.go                  # seeded：customOptions / variantOpts
│   ├── register_grpcgw.go         # //go:build grpcgw：variantOpts["grpcgw"]
│   └── register_nacos.go          # //go:build nacos
├── api/
│   ├── proto/greet.proto          # 真实 proto + google.api.http 注解
│   ├── buf.yaml / buf.gen.yaml    # seeded（go / go-grpc / grpc-gateway 三插件）
│   └── gen/                       # buf generate 产物（.pb.go / _grpc.pb.go / _pb.gw.go；
│                                  #   包名 baldv1，paths=source_relative 落在 gen/ 根）
└── taskfile.yml                   # `task proto`（dir: api）
```

---

## 2. proto 定义（含 transcoding）

`api/proto/greet.proto` 关键片段：

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

```bash
# buf：见 https://buf.build/docs/installation
# 插件走 buf remote（buf.gen.yaml 的 remote:），无需本机 protoc-gen-*。
```

生成：

```bash
cd _example/bald
task proto        # 等价于 cd api && buf generate
go mod tidy       # 首次会把 grpc-gateway/v2 等写入 go.mod
```

产物 `api/gen/`（Go 包名 `baldv1`，导入路径 `github.com/kalandramo/bald/example/bald/api/gen`）：
- `greet.pb.go`：message 与 `GreetServiceServer` 接口
- `greet_grpc.pb.go`：`RegisterGreetServiceServer` / `ServiceDesc`
- `greet.pb.gw.go`：`RegisterGreetServiceHandler(ctx, mux, conn)`

---

## 4. 接线到 bald server（框架契约）

transport module（自 pkg/server 迁入）提供两个载体：

- **gRPC**（子包 `transport/grpc`，包名 `grpcserver`）：
  `grpcserver.NewGRPCServerWithRegister(cfg *bootstrapv1.Server_Grpc, unary []grpc.ServerOption, register func(s *grpc.Server))` —— 包装新建的 `*grpc.Server`，注册 gRPC 标准健康服务（只注册不判断就绪），`register` 回调里挂业务 service；reflection 与就绪推送由装配层接线。
- **gateway**（子包 `transport/gateway`，包名 `gateway`）：
  `gateway.NewGatewayServer(httpCfg *bootstrapv1.Server_Http, grpcBackend *bootstrapv1.Server_Grpc, register func(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error))` —— `register` 回调**自建** `runtime.ServeMux` 并作为 `http.Handler` 返回（`transport` 因此不必依赖 grpc-gateway）；探针在装配层包在这个返回值外层。

  其中 `grpcBackend` 是 `*bootstrapv1.Server_Grpc` 指针（**不是**固化字符串）：网关在 `Start`
  时才读 `grpcBackend.GetAddr()`，因此 env/flag 对 `grpc.addr` 的覆盖能正确生效；
  若传构造期快照字符串，会锁死默认端口（如 `:9090`），导致测试用 `t.Setenv`
  覆盖地址无效。

`register_grpcgw.go`（build tag `grpcgw`）即此接线示例：业务实现随域
（`greetv1.Domain.Services` / `Domain.RouteTable`），应用层只登记网关面。

```go
//go:build grpcgw

import gatewaycontract "github.com/kalandramo/bald/transport/gateway/contract"

func init() {
	variantOpts["grpcgw"] = []appkit.Option{
		appkit.OnRole(RoleAPI,
			appkit.WithHTTP(nil),                        // 让出 gin 面
			gatewaycontract.HTTPProvider(registerGateway), // 接管 server.http 段
		),
	}
}

// registerGateway 自建 grpc-gateway v2 的 runtime.ServeMux，注册 handler 后
// 作为 http.Handler 交回 GatewayServer。
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

默认构建走 gin 业务面（`appkit.WithHTTPContract(apiserver.NewRouter)` + 域自持 REST 路由，横切链按契约 `server.http.middleware` 段构造）。启用完整
transcoding 只需 `-tags grpcgw`：`variant_grpcgw.gen.go` 编入实现，`register_grpcgw.go`
把增量登记到 `variantOpts["grpcgw"]`，`assemble.gen.go` 的 `variantOptions()` 在
`Options()` 调用期展开——API 角色经 `WithHTTP(nil)` 让出 gin 面，`HTTPProvider`
声明在 `std.Standard()` 之后即按段路径末者胜接管 `server.http`，该端口装配为
网关转码面。

一处已知成本：契约钩子的求值点在变体之前（`pkg/appkit/contract_faces.go`），所以转码形态下 `apiserver.NewRouter` 仍会被调用一次，建出的 gin 链与限流桶随后由 `WithHTTP(nil)` 弃用（该 Option 同时清掉钩子声明，面不会被装回来）。`close` 照常进停机 Effect，不泄漏，只是白建一次；`server.http.middleware` 段在这条路径上不影响转码面——转码面没有 gin 链。

```bash
cd _example/bald
go run -tags grpcgw ./cmd/bald-demo-api    # 配置随示例自带，自动加载
```

---

## 6. 验证 transcoding

```bash
# gRPC（需 grpcurl / 客户端）
grpcurl -plaintext -d '{"name":"bald"}' localhost:9090 bald.v1.GreetService/Greet

# REST / JSON（transcoding；driver=grpc-gateway 时 server.http 端口即转码面）
curl -i -XPOST http://127.0.0.1:8080/v1/greet -H "Content-Type: application/json" -d '{"name":"bald"}'
# => {"greet":"hello, bald"}

curl -i http://127.0.0.1:8080/v1/greet/bald
# => {"greet":"hello, bald"}
```

同一份 biz 函数 `Greet`，既服务于 grpc-gateway transcoding（REST→gRPC），也可被 gin 的 `web.HandleJSONRequest`（默认构建的 HTTP 直连）调用——**传输面可切换，biz 层不变**。

---

## 7. 约定

1. **transcoding 规则写在 proto 的 `google.api.http` 注解里**，不要手写 REST handler。
2. **biz 函数与传输层解耦**：gRPC 实现层只做"取字段 → 调 biz → 组装响应"的薄适配。
3. **gateway 与 gRPC 同进程**：`GatewayServer` 在 `Start` 时读 `grpcBackend.GetAddr()` 连本进程 gRPC，无需独立部署。
4. **转码面是 `server.http` 段的 Provider**：`HTTPProvider(register)` 声明在 `std.Standard()` 之后即接管该段；同时保留 `WithHTTP` 的业务 handler 时 `server.http.driver` 必须显式写 `grpc-gateway`（构造期 fail-fast，显式 > 隐式）。独立端口拓扑（gin 主面 + 并存转码面）改用 `server.gateway` 段 + `AppProvider(register)`。`WithExtraServerFunc` 是契约形状表达不了的服务器的通用逃生舱。
5. **启用需 build tag**：示例接线文件用 `//go:build grpcgw` 保护，避免无 protoc 环境编译失败。
