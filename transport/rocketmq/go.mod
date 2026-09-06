module github.com/kalandramo/bald/transport/rocketmq

go 1.26.5

replace github.com/kalandramo/bald => ../../

replace github.com/kalandramo/bald/bconf => ../../bconf

replace github.com/kalandramo/bald/broker => ../../broker

replace github.com/kalandramo/bald/broker/rocketmq => ../../broker/rocketmq

replace github.com/kalandramo/bald/encoding => ../../encoding

replace github.com/kalandramo/bald/encoding/json => ../../encoding/json

replace github.com/kalandramo/bald/encoding/proto => ../../encoding/proto

replace github.com/kalandramo/bald/log => ../../log

replace github.com/kalandramo/bald/metrics => ../../metrics

replace github.com/kalandramo/bald/transport => ..

replace github.com/kalandramo/bald/transport/subscribe => ../subscribe

require (
	github.com/kalandramo/bald/broker v0.0.0
	github.com/kalandramo/bald/broker/rocketmq v0.0.0-00010101000000-000000000000
	github.com/kalandramo/bald/log v0.0.0
	github.com/kalandramo/bald/metrics v0.0.0-00010101000000-000000000000
	github.com/kalandramo/bald/transport/subscribe v0.0.0-00010101000000-000000000000
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/apache/rocketmq-client-go/v2 v2.1.2 // indirect
	github.com/emirpasic/gods v1.18.1 // indirect
	github.com/golang/mock v1.6.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/kalandramo/bald/encoding v0.0.0 // indirect
	github.com/kalandramo/bald/encoding/json v0.0.0 // indirect
	github.com/kalandramo/bald/encoding/proto v0.0.0 // indirect
	github.com/konsorten/go-windows-terminal-sequences v1.0.1 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/patrickmn/go-cache v2.1.0+incompatible // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/sirupsen/logrus v1.4.0 // indirect
	github.com/smarty/assertions v1.16.0 // indirect
	github.com/tidwall/gjson v1.17.3 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/term v0.45.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	stathat.com/c/consistent v1.0.0 // indirect
)

replace github.com/gogap/errors => github.com/gogap/errors v0.0.0-20210818113853-edfbba0ddea9
