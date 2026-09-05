// _example 是独立 Go module，不是核心 module 的一部分。
//
// 为什么独立（P5 依赖治理）：
// example 需要 grpc-gateway/v2、cel-go（protovalidate）等较重的依赖。
// 若放在核心 module 里，这些依赖会进入核心依赖图，所有使用 bald 的项目都会被
// 迫传递依赖它们。独立成 module 后，重依赖只存在于 example 的 go.sum，
// 核心 go.mod 保持最小。
//
// 与 tests/integration 采用同一范式：module + replace 指向本地 bald。
// 注意 replace 的相对路径取决于 go.mod 所在层级：本文件在 _example/（一级），
// 所以用 `..`；tests/integration 在两级目录下，用 `../..`。
//
// 构建：cd _example && go build ./...（注意 _example 以下划线开头，
// 核心 module 的 `go build ./...` 不会包含它）。
module github.com/kalandramo/bald/example

go 1.26.5

require (
	github.com/gin-gonic/gin v1.12.0
	github.com/kalandramo/bald v0.0.0
	github.com/kalandramo/bald/bconf v0.0.0-00010101000000-000000000000
	github.com/kalandramo/bald/log v0.0.0
	github.com/kalandramo/bald/transport v0.0.0
	github.com/spf13/pflag v1.0.10
)

require gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect

require (
	github.com/bytedance/gopkg v0.1.3 // indirect
	github.com/bytedance/sonic v1.15.0 // indirect
	github.com/bytedance/sonic/loader v0.5.0 // indirect
	github.com/cloudwego/base64x v0.1.6 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/gabriel-vasile/mimetype v1.4.12 // indirect
	github.com/gin-contrib/sse v1.1.0 // indirect
	github.com/go-kratos/kratos/v3 v3.0.0 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
	github.com/go-playground/validator/v10 v10.30.1 // indirect
	github.com/go-viper/mapstructure/v2 v2.4.0 // indirect
	github.com/goccy/go-json v0.10.5 // indirect
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/kalandramo/bald/berrors v0.0.0 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/leodido/go-urn v1.4.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/pelletier/go-toml/v2 v2.2.4 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/quic-go/quic-go v0.59.0 // indirect
	github.com/sagikazarmark/locafero v0.11.0 // indirect
	github.com/sourcegraph/conc v0.3.1-0.20240121214520-5f936abd7ae8 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	github.com/spf13/viper v1.21.0 // indirect
	github.com/subosito/gotenv v1.6.0 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	github.com/ugorji/go/codec v1.3.1 // indirect
	go.mongodb.org/mongo-driver/v2 v2.5.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/arch v0.22.0 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// _example/go.mod 位于 bald 仓库根之下，向上一级（..）即 bald 模块根。
// 注意：tests/integration 是两级目录（tests/integration/），它才用 ../..。
replace github.com/kalandramo/bald => ..

replace github.com/kalandramo/bald/bconf => ../bconf

replace github.com/kalandramo/bald/bootstrap => ../bootstrap

replace github.com/kalandramo/bald/bconfig => ../bconfig


replace github.com/kalandramo/bald/berrors => ../berrors

replace github.com/kalandramo/bald/log => ../log

replace github.com/kalandramo/bald/transport => ../transport

replace github.com/kalandramo/bald-store-gorm => ../contrib/store-gorm

replace github.com/go-kratos/kratos/v3/contrib/registry/nacos/v3 => ../../kratos/contrib/registry/nacos

replace github.com/go-kratos/kratos/v3/contrib/config/nacos/v3 => ../../kratos/contrib/config/nacos
