module github.com/kalandramo/bald

go 1.27.1

replace github.com/kalandramo/bald/bconf => ./bconf

replace github.com/kalandramo/bald/bconfig => ./bconfig

replace github.com/kalandramo/bald/bootstrap => ./bootstrap

replace github.com/kalandramo/bald/berrors => ./berrors

replace github.com/kalandramo/bald/health => ./health

replace github.com/kalandramo/bald/log => ./log

replace github.com/kalandramo/bald/log/aliyun => ./log/aliyun

replace github.com/kalandramo/bald/log/charm => ./log/charm

replace github.com/kalandramo/bald/log/loki => ./log/loki

replace github.com/kalandramo/bald/log/sentry => ./log/sentry

replace github.com/kalandramo/bald/log/tencent => ./log/tencent

replace github.com/kalandramo/bald/registry => ./registry

replace github.com/kalandramo/bald/transport => ./transport

replace github.com/kalandramo/bald/transport/gateway => ./transport/gateway

replace github.com/kalandramo/bald/transport/grpc => ./transport/grpc

replace github.com/kalandramo/bald/transport/http => ./transport/http

replace github.com/kalandramo/bald/transport/web => ./transport/web

require (
	github.com/kalandramo/bald/bconf v0.15.3
	github.com/spf13/cobra v1.10.2
	google.golang.org/protobuf v1.36.12
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)

replace github.com/kalandramo/bald/ratelimit => ./ratelimit

replace github.com/kalandramo/bald/ratelimit/tokenbucket => ./ratelimit/tokenbucket
