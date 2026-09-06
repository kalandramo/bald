module github.com/kalandramo/bald/log/tencent

go 1.26.5

require (
	github.com/kalandramo/bald/bconf v0.0.0
	github.com/kalandramo/bald/log v0.0.0
	github.com/tencentcloud/tencentcloud-cls-sdk-go v1.0.14
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/klauspost/compress v1.18.5 // indirect
	github.com/pierrec/lz4 v2.6.1+incompatible // indirect
	go.uber.org/atomic v1.11.0 // indirect
)

replace github.com/kalandramo/bald/bconf => ../../bconf

replace github.com/kalandramo/bald/log => ..
