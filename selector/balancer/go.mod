module github.com/kalandramo/bald/selector/balancer

go 1.27.1

require (
	github.com/kalandramo/bald/selector v0.1.1
	google.golang.org/grpc v1.83.2
)

// 与 33 个适配器同款：同仓兄弟模块用本地源码路径跟踪（replace 只在本
// module 作为主模块时生效，消费者不受影响）。发布随 selector tag 之后走。
replace github.com/kalandramo/bald/selector => ../

require (
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)
