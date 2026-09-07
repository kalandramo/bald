module github.com/kalandramo/bald/transport/subscribe

go 1.27.1

require github.com/kalandramo/bald/broker v0.0.0-00010101000000-000000000000

require (
	github.com/kalandramo/bald/encoding v0.0.0 // indirect
	github.com/kalandramo/bald/encoding/json v0.0.0 // indirect
	github.com/kalandramo/bald/encoding/proto v0.0.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace github.com/kalandramo/bald/broker => ../../broker

replace github.com/kalandramo/bald/encoding => ../../encoding

replace github.com/kalandramo/bald/encoding/json => ../../encoding/json

replace github.com/kalandramo/bald/encoding/proto => ../../encoding/proto
