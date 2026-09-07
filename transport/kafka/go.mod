module github.com/kalandramo/bald/transport/kafka

go 1.27.1

replace github.com/kalandramo/bald => ../../

replace github.com/kalandramo/bald/bconf => ../../bconf

replace github.com/kalandramo/bald/broker => ../../broker

replace github.com/kalandramo/bald/broker/kafka => ../../broker/kafka

replace github.com/kalandramo/bald/encoding => ../../encoding

replace github.com/kalandramo/bald/encoding/json => ../../encoding/json

replace github.com/kalandramo/bald/encoding/proto => ../../encoding/proto

replace github.com/kalandramo/bald/log => ../../log

replace github.com/kalandramo/bald/metrics => ../../metrics

replace github.com/kalandramo/bald/transport => ..

replace github.com/kalandramo/bald/transport/subscribe => ../subscribe

require (
	github.com/kalandramo/bald/broker v0.0.0
	github.com/kalandramo/bald/broker/kafka v0.0.0-00010101000000-000000000000
	github.com/kalandramo/bald/log v0.0.0
	github.com/kalandramo/bald/metrics v0.0.0-00010101000000-000000000000
	github.com/kalandramo/bald/transport/subscribe v0.0.0-00010101000000-000000000000
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/kalandramo/bald/encoding v0.0.0 // indirect
	github.com/kalandramo/bald/encoding/json v0.0.0 // indirect
	github.com/kalandramo/bald/encoding/proto v0.0.0 // indirect
	github.com/klauspost/compress v1.17.9 // indirect
	github.com/pierrec/lz4/v4 v4.1.21 // indirect
	github.com/segmentio/kafka-go v0.4.49 // indirect
	github.com/xdg-go/pbkdf2 v1.0.0 // indirect
	github.com/xdg-go/scram v1.1.2 // indirect
	github.com/xdg-go/stringprep v1.0.4 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
