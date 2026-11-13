module github.com/kalandramo/bald/transport/http

go 1.27.1

replace github.com/kalandramo/bald/bconf => ../../bconf

replace github.com/kalandramo/bald/transport => ..

require (
	github.com/kalandramo/bald/bconf v0.28.0
	github.com/kalandramo/bald/transport v0.5.0
)

require google.golang.org/protobuf v1.36.12 // indirect
