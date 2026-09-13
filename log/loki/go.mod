module github.com/kalandramo/bald/log/loki

go 1.27.1

require (
	github.com/kalandramo/bald/bconf v0.1.0
	github.com/kalandramo/bald/log v0.3.0
)

require google.golang.org/protobuf v1.36.11 // indirect

replace github.com/kalandramo/bald/bconf => ../../bconf

replace github.com/kalandramo/bald/log => ..
