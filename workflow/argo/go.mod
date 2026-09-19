module github.com/kalandramo/bald/workflow/argo

go 1.27.1

require (
	github.com/kalandramo/bald/bconf v0.7.2
	github.com/kalandramo/bald/log v0.5.1
)

require google.golang.org/protobuf v1.36.11 // indirect

replace github.com/kalandramo/bald/bconf => ../../bconf

replace github.com/kalandramo/bald/log => ../../log
