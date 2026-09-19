module github.com/kalandramo/bald/transport/http

go 1.27.1

replace github.com/kalandramo/bald/bconf => ../../bconf

replace github.com/kalandramo/bald/transport => ..

require (
	github.com/kalandramo/bald/bconf v0.7.2
	github.com/kalandramo/bald/transport v0.2.1
)

require google.golang.org/protobuf v1.36.11 // indirect
