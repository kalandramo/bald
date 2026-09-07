module github.com/kalandramo/bald/transport/gateway

go 1.27.1

replace github.com/kalandramo/bald/bconf => ../../bconf

replace github.com/kalandramo/bald/transport => ..

replace github.com/kalandramo/bald/transport/http => ../http

require (
	github.com/kalandramo/bald/bconf v0.0.0-00010101000000-000000000000
	github.com/kalandramo/bald/transport v0.0.0-00010101000000-000000000000
	github.com/kalandramo/bald/transport/http v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.83.2
)

require (
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)
