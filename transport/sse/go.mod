module github.com/kalandramo/bald/transport/sse

go 1.27.1

replace github.com/kalandramo/bald/transport => ..

replace github.com/kalandramo/bald/encoding => ../../encoding

replace github.com/kalandramo/bald/encoding/json => ../../encoding/json

require (
	github.com/google/uuid v1.6.0
	github.com/kalandramo/bald/bconf v0.9.2
	github.com/kalandramo/bald/encoding v0.1.0
	github.com/kalandramo/bald/encoding/json v0.1.1
	github.com/kalandramo/bald/transport v0.2.1
)

require google.golang.org/protobuf v1.36.11 // indirect

replace github.com/kalandramo/bald/bconf => ../../bconf
