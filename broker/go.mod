module github.com/kalandramo/bald/broker

go 1.27.1

require (
	github.com/kalandramo/bald/encoding v0.1.0
	github.com/kalandramo/bald/encoding/json v0.1.1
	github.com/kalandramo/bald/encoding/proto v0.1.0
)

require google.golang.org/protobuf v1.36.11 // indirect

replace github.com/kalandramo/bald/encoding => ../encoding

replace github.com/kalandramo/bald/encoding/json => ../encoding/json

replace github.com/kalandramo/bald/encoding/proto => ../encoding/proto
