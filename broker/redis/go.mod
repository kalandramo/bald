module github.com/kalandramo/bald/broker/redis

go 1.27.1

require (
	github.com/alicebob/miniredis/v2 v2.39.0
	github.com/gomodule/redigo v1.9.2
	github.com/kalandramo/bald/bconf v0.0.0
	github.com/kalandramo/bald/broker v0.0.0
	github.com/kalandramo/bald/log v0.0.0
)

require (
	github.com/kalandramo/bald/encoding v0.0.0 // indirect
	github.com/kalandramo/bald/encoding/json v0.0.0 // indirect
	github.com/kalandramo/bald/encoding/proto v0.0.0 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/kalandramo/bald => ../../

replace github.com/kalandramo/bald/bconf => ../../bconf

replace github.com/kalandramo/bald/broker => ../

replace github.com/kalandramo/bald/log => ../../log

replace github.com/kalandramo/bald/encoding => ../../encoding

replace github.com/kalandramo/bald/encoding/json => ../../encoding/json

replace github.com/kalandramo/bald/encoding/proto => ../../encoding/proto
