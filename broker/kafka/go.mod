module github.com/kalandramo/bald/broker/kafka

go 1.27.1

require (
	github.com/google/uuid v1.6.0
	github.com/kalandramo/bald/bconf v0.7.2
	github.com/kalandramo/bald/broker v0.1.0
	github.com/kalandramo/bald/log v0.5.1
	github.com/segmentio/kafka-go v0.4.49
)

require (
	github.com/kalandramo/bald/encoding v0.1.0 // indirect
	github.com/kalandramo/bald/encoding/json v0.1.1 // indirect
	github.com/kalandramo/bald/encoding/proto v0.1.0 // indirect
	github.com/klauspost/compress v1.17.9 // indirect
	github.com/pierrec/lz4/v4 v4.1.21 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	github.com/xdg-go/pbkdf2 v1.0.0 // indirect
	github.com/xdg-go/scram v1.1.2 // indirect
	github.com/xdg-go/stringprep v1.0.4 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/kalandramo/bald => ../../

replace github.com/kalandramo/bald/bconf => ../../bconf

replace github.com/kalandramo/bald/broker => ../

replace github.com/kalandramo/bald/log => ../../log

replace github.com/kalandramo/bald/encoding => ../../encoding

replace github.com/kalandramo/bald/encoding/json => ../../encoding/json

replace github.com/kalandramo/bald/encoding/proto => ../../encoding/proto
