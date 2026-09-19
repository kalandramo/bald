module github.com/kalandramo/bald/transport/rabbitmq

go 1.27.1

replace github.com/kalandramo/bald => ../../

replace github.com/kalandramo/bald/bconf => ../../bconf

replace github.com/kalandramo/bald/broker => ../../broker

replace github.com/kalandramo/bald/broker/rabbitmq => ../../broker/rabbitmq

replace github.com/kalandramo/bald/encoding => ../../encoding

replace github.com/kalandramo/bald/encoding/json => ../../encoding/json

replace github.com/kalandramo/bald/encoding/proto => ../../encoding/proto

replace github.com/kalandramo/bald/log => ../../log

replace github.com/kalandramo/bald/metrics => ../../metrics

replace github.com/kalandramo/bald/transport => ..

replace github.com/kalandramo/bald/transport/subscribe => ../subscribe

require (
	github.com/kalandramo/bald/broker v0.1.0
	github.com/kalandramo/bald/broker/rabbitmq v0.1.0
	github.com/kalandramo/bald/log v0.5.1
	github.com/kalandramo/bald/metrics v0.1.0
	github.com/kalandramo/bald/transport/subscribe v0.1.0
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/kalandramo/bald/encoding v0.1.0 // indirect
	github.com/kalandramo/bald/encoding/json v0.1.1 // indirect
	github.com/kalandramo/bald/encoding/proto v0.1.0 // indirect
	github.com/rabbitmq/amqp091-go v1.10.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
