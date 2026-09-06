module github.com/kalandramo/bald/broker/rabbitmq

go 1.26.5

require (
	github.com/google/uuid v1.6.0
	github.com/kalandramo/bald/bconf v0.0.0
	github.com/kalandramo/bald/broker v0.0.0
	github.com/kalandramo/bald/log v0.0.0
	github.com/rabbitmq/amqp091-go v1.10.0
)

require (
	github.com/kalandramo/bald/encoding v0.0.0 // indirect
	github.com/kalandramo/bald/encoding/json v0.0.0 // indirect
	github.com/kalandramo/bald/encoding/proto v0.0.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/kalandramo/bald => ../../

replace github.com/kalandramo/bald/bconf => ../../bconf

replace github.com/kalandramo/bald/broker => ../

replace github.com/kalandramo/bald/log => ../../log

replace github.com/kalandramo/bald/encoding => ../../encoding

replace github.com/kalandramo/bald/encoding/json => ../../encoding/json

replace github.com/kalandramo/bald/encoding/proto => ../../encoding/proto
