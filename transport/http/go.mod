module github.com/kalandramo/bald/transport/http

go 1.27.1

replace github.com/kalandramo/bald/bconf => ../../bconf

replace github.com/kalandramo/bald/transport => ..

require (
	github.com/kalandramo/bald/bconf v0.0.0-00010101000000-000000000000
	github.com/kalandramo/bald/transport v0.0.0-00010101000000-000000000000
)

require google.golang.org/protobuf v1.36.11 // indirect
