module github.com/kalandramo/bald/log/loki

go 1.26.5

require (
	github.com/kalandramo/bald/bconf v0.0.0-00010101000000-000000000000
	github.com/kalandramo/bald/log v0.0.0
)

require google.golang.org/protobuf v1.36.11 // indirect

replace github.com/kalandramo/bald/bconf => ../../bconf

replace github.com/kalandramo/bald/log => ..
