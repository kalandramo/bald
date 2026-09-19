module github.com/kalandramo/bald/transport/redis

go 1.27.1

replace github.com/kalandramo/bald => ../../

replace github.com/kalandramo/bald/bconf => ../../bconf

replace github.com/kalandramo/bald/broker => ../../broker

replace github.com/kalandramo/bald/broker/redis => ../../broker/redis

replace github.com/kalandramo/bald/encoding => ../../encoding

replace github.com/kalandramo/bald/encoding/json => ../../encoding/json

replace github.com/kalandramo/bald/encoding/proto => ../../encoding/proto

replace github.com/kalandramo/bald/log => ../../log

replace github.com/kalandramo/bald/metrics => ../../metrics

replace github.com/kalandramo/bald/transport => ..

replace github.com/kalandramo/bald/transport/subscribe => ../subscribe

require (
	github.com/kalandramo/bald/broker v0.1.0
	github.com/kalandramo/bald/broker/redis v0.1.0
	github.com/kalandramo/bald/log v0.5.1
	github.com/kalandramo/bald/metrics v0.1.0
	github.com/kalandramo/bald/transport/subscribe v0.1.0
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/gomodule/redigo v1.9.2 // indirect
	github.com/kalandramo/bald/encoding v0.1.0 // indirect
	github.com/kalandramo/bald/encoding/json v0.1.1 // indirect
	github.com/kalandramo/bald/encoding/proto v0.1.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
