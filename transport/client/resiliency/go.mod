module github.com/kalandramo/bald/transport/client/resiliency

go 1.27.1

require (
	github.com/kalandramo/bald/circuitbreaker v0.2.0
	github.com/kalandramo/bald/circuitbreaker/hystrix v0.1.1
	github.com/kalandramo/bald/retry v0.3.1
	google.golang.org/grpc v1.83.2
	google.golang.org/protobuf v1.36.11
)

require (
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
)

// 与 selector/circuitbreaker 同款桥接子 module：同仓兄弟模块用本地源码路径
// 跟踪（replace 只在本 module 作为主模块时生效，消费者不受影响）。本包刻意
// 不 import transport/client——只产出原生 grpc.DialOption / http.RoundTripper，
// 由调用方经 client.WithDialOptions / client.WithTransport 组合，依赖面保持轻量。
replace github.com/kalandramo/bald/circuitbreaker => ../../../circuitbreaker

replace github.com/kalandramo/bald/circuitbreaker/hystrix => ../../../circuitbreaker/hystrix

replace github.com/kalandramo/bald/retry => ../../../retry
