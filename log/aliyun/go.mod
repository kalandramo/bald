module github.com/kalandramo/bald/log/aliyun

go 1.27.1

require (
	github.com/aliyun/aliyun-log-go-sdk v0.1.131
	github.com/kalandramo/bald/bconf v0.7.2
	github.com/kalandramo/bald/log v0.5.1
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/cenkalti/backoff v2.2.1+incompatible // indirect
	github.com/go-kit/kit v0.13.0 // indirect
	github.com/go-kit/log v0.2.1 // indirect
	github.com/go-logfmt/logfmt v0.6.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/klauspost/compress v1.17.9 // indirect
	github.com/pierrec/lz4/v4 v4.1.22 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/stretchr/testify v1.9.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/net v0.60.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
)

replace github.com/kalandramo/bald/bconf => ../../bconf

replace github.com/kalandramo/bald/log => ..
