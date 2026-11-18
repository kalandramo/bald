module github.com/kalandramo/bald/selector/circuitbreaker

go 1.27.1

require (
	github.com/alibaba/sentinel-golang v1.0.4
	github.com/kalandramo/bald/berrors v0.1.2
	github.com/kalandramo/bald/circuitbreaker v0.2.1
	github.com/kalandramo/bald/circuitbreaker/sentinel v0.1.1
	github.com/kalandramo/bald/selector v0.1.1
	google.golang.org/grpc v1.83.2
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
	golang.org/x/sys v0.47.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260904194346-d0f1323225a4 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/yaml.v2 v2.3.0 // indirect
)

// 与 selector/balancer 同款：同仓兄弟模块用本地源码路径跟踪（replace 只在
// 本 module 作为主模块时生效，消费者不受影响）。发布随 selector tag 之后走。

replace github.com/kalandramo/bald/selector => ../

replace github.com/kalandramo/bald/berrors => ../../berrors

replace github.com/kalandramo/bald/circuitbreaker => ../../circuitbreaker
