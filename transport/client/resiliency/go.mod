module github.com/kalandramo/bald/transport/client/resiliency

go 1.27.1

require (
	github.com/alibaba/sentinel-golang v1.0.4
	github.com/kalandramo/bald/circuitbreaker v0.2.0
	github.com/kalandramo/bald/circuitbreaker/sentinel v0.1.0
	github.com/kalandramo/bald/retry v0.3.1
	google.golang.org/grpc v1.83.2
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/StackExchange/wmi v0.0.0-20190523213315-cbe66965904d // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-ole/go-ole v1.2.4 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/prometheus/client_golang v1.23.2 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.66.1 // indirect
	github.com/prometheus/procfs v0.16.1 // indirect
	github.com/shirou/gopsutil/v3 v3.21.6 // indirect
	github.com/tklauser/go-sysconf v0.3.6 // indirect
	github.com/tklauser/numcpus v0.2.2 // indirect
	go.yaml.in/yaml/v2 v2.4.2 // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	gopkg.in/yaml.v2 v2.3.0 // indirect
)

// 与 selector/circuitbreaker 同款桥接子 module：同仓兄弟模块用本地源码路径
// 跟踪（replace 只在本 module 作为主模块时生效，消费者不受影响）。本包刻意
// 不 import transport/client——只产出原生 grpc.DialOption / http.RoundTripper，
// 由调用方经 client.WithDialOptions / client.WithTransport 组合，依赖面保持轻量。
replace github.com/kalandramo/bald/circuitbreaker => ../../../circuitbreaker

replace github.com/kalandramo/bald/circuitbreaker/sentinel => ../../../circuitbreaker/sentinel

replace github.com/kalandramo/bald/retry => ../../../retry
