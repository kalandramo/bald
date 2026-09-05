module github.com/kalandramo/bald

go 1.26.5

replace github.com/kalandramo/bald/bconf => ./bconf

replace github.com/kalandramo/bald/bconfig => ./bconfig

replace github.com/kalandramo/bald/bootstrap => ./bootstrap

replace github.com/kalandramo/bald/berrors => ./berrors

replace github.com/kalandramo/bald/log => ./log

replace github.com/kalandramo/bald/transport => ./transport

replace github.com/kalandramo/bald-crud/viewer => ../bald-crud/viewer

require (
	github.com/gin-gonic/gin v1.12.0
	github.com/go-kratos/kratos/v3 v3.0.0
	github.com/google/uuid v1.6.0
	github.com/kalandramo/bald-crud/viewer v0.0.0-00010101000000-000000000000
	github.com/kalandramo/bald/bconf v0.0.0-00010101000000-000000000000
	github.com/kalandramo/bald/berrors v0.0.0
	github.com/kalandramo/bald/bootstrap v0.0.0-00010101000000-000000000000
	github.com/kalandramo/bald/log v0.0.0
	github.com/kalandramo/bald/transport v0.0.0-00010101000000-000000000000
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.0.10
	github.com/stretchr/testify v1.12.1
	go.opentelemetry.io/otel v1.46.0
	go.opentelemetry.io/otel/metric v1.46.0
	go.opentelemetry.io/otel/trace v1.46.0
	golang.org/x/sync v0.22.0
	google.golang.org/grpc v1.83.2
	google.golang.org/protobuf v1.36.12
	k8s.io/klog/v2 v2.140.0
)

require (
	github.com/bytedance/gopkg v0.1.3 // indirect
	github.com/bytedance/sonic v1.15.0 // indirect
	github.com/bytedance/sonic/loader v0.5.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cloudwego/base64x v0.1.6 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/gabriel-vasile/mimetype v1.4.12 // indirect
	github.com/gin-contrib/sse v1.1.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
	github.com/go-playground/validator/v10 v10.30.1 // indirect
	github.com/goccy/go-json v0.10.5 // indirect
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/kalandramo/bald/bconfig v0.0.0-00010101000000-000000000000 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/leodido/go-urn v1.4.0 // indirect
	github.com/mattn/go-isatty v0.0.22 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/pelletier/go-toml/v2 v2.2.4 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/quic-go/quic-go v0.59.0 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	github.com/ugorji/go/codec v1.3.1 // indirect
	go.mongodb.org/mongo-driver/v2 v2.5.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/arch v0.22.0 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260904194346-d0f1323225a4 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
