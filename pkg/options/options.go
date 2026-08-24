// Package options 提供 bald 框架各协议服务器所需的配置选项。
// 设计对齐 onexstack/pkg/options：统一的 IOptions 接口、AddFlags(fs, fullPrefix)
// 前缀式 flag 注册、Validate() []error 校验，以及可复用的地址校验/监听工具。
//
// 文件组织（与 onexstack 对称）：
//   - options.go            ：本文件，仅定义 IOptions 接口契约
//   - insecure_serving.go   ：InsecureServingOptions（明文 HTTP）
//   - secure_serving.go     ：SecureServingOptions（HTTPS，内嵌 TLSOptions）
//   - tls_options.go        ：TLSOptions（Smart Mode：路径/PEM/Base64）
//   - grpc_options.go       ：GRPCOptions（gRPC 监听与超时）
//   - helper.go             ：Join / ValidateAddress / CreateListener
package options

import "github.com/spf13/pflag"

// IOptions 定义通用 options 的方法契约。
// 任何业务 options 结构体实现 Validate + AddFlags 即可被统一编排
// （如 appkit 遍历 options 注册 flag、启动前统一校验）。
type IOptions interface {
	// Validate 校验所有必填项，必要时可补全默认值。
	// 返回 error 切片（而非单个 error），便于一次暴露全部问题。
	Validate() []error

	// AddFlags 将字段注册为命令行 flag。
	// fullPrefix 是完整前缀（如 "bald-demo.http"），实现体在其后追加自身字段名，
	// 形成 --bald-demo.http.addr 这类嵌套 flag，支持同一 options 类型被多组件复用。
	AddFlags(fs *pflag.FlagSet, fullPrefix string)
}
