// bald 架构模型（现状视图）— Structurizr DSL
// 校验方式：structurizr-cli validate -in bald-c4.dsl   （本机无 graphviz/structurizr CLI，此文件尚未机器校验）
// 证据日期：2026-11-01，main @ ca981b1

workspace "bald" "bald Go 多模块框架 — 当前架构" "1.0" {

    model {

        // === People ===
        person svc_dev "业务服务开发者" "用 appkit/std 装配一个可运行服务，或在 biz/ 域包里写业务逻辑"
        person fw_maint "框架维护者" "维护 SPI、Provider、codegen 与多模块发布"

        // === External systems ===
        system user_service "业务服务进程" "消费方：_example/bald（github.com/kalandramo/bald/example/bald）与 bald-admin"
        system etcd_nacos "配置/注册中心" "etcd / consul / nacos / kubernetes，由 bconfig 与 registry 适配器对接"
        system mq "消息中间件" "Kafka / RabbitMQ / RocketMQ / Redis，由 broker 适配器对接"
        system otel_backend "OTLP 后端" "trace/metrics 导出目标，经 metrics/otel 与 contrib/observability-otlp"

        // === The system ===
        system bald "bald 框架仓库" "SPI + 适配器 + 声明式装配 + 代码生成（93 个 go.mod）" {

            spi_core = container "spi_core" "SPI 契约模块" "Go module" "只定义接口。transport/server.go:14-22 = Start/Stop/Endpoint；broker/broker.go:9-33 = Broker；cache/cache.go:28-69；registry/registry.go:62-75；metrics/metrics.go:15-19"
            bconf = container "bconf" "bconf 配置契约" "Go module" "protobuf 生成的强类型契约（bconf/proto → bconf/gen）：bootstrapv1.BootstrapConfig + appspec/v1"
            bconfig = container "bconfig" "bconfig 配置源抽象" "Go module" "Reader/Watcher/Decoder（bconfig/bconfig.go:3-16）+ file/env/etcd/consul/nacos/vault/apollo"
            bootstrap = container "bootstrap" "bootstrap 装配桥" "Go module" "分层合并内核（bootstrap/config/config.go:1-17，优先级 flag > env > file > contract source > remote bridge）+ ServerProvider 工厂（bootstrap/server.go:31,100,154）+ 远程配置驱动"
            appkit = container "appkit" "pkg/appkit 组合层" "根 module 内包" "v2 Provider Plan/Build + Segments（pkg/appkit/provider.go:1-24）、Run/stopAll 生命周期（pkg/appkit/appkit.go:561-703,715-750）、std 标准装配"
            middleware = container "middleware" "pkg/middleware 横切层" "根 module 内包" "bundle 链序固化：Gin() / GRPCInterceptors()（pkg/middleware/bundle/bundle.go:171-211,224-241）"
            adapters = container "adapters" "适配器模块" "Go module × N" "transport/*(18) broker/*(4) cache/*(3) registry/*(4) log/*(5) metrics/*(3) encoding/*(12) ratelimit/*(3) circuitbreaker/*(4) oss ai workflow cobramcp"
            contract = container "contract" "*/contract Provider 注册包" "适配器 module 内子包 × 30" "适配器接入 appkit v2 Provider 面的唯一接缝，如 transport/gateway/contract/provider.go:24、registry/etcd/contract/provider.go:16"
            codegen = container "codegen" "cmd/bald + internal/codegen" "根 module" "bald gen app|domain|proto|store：从 appspec.json 产出 owned/seeded 文件树"
            contrib = container "contrib" "contrib 扩展" "Go module" "store-gorm/mongo、audit-*、authn-jwt、authz-casbin、config-nacos、observability-otlp、database"
        }

        // === Relationships ===
        svc_dev -> user_service "编写业务域包与配置"
        fw_maint -> codegen "维护生成器与契约"

        user_service -> appkit "Run(ctx, WithHTTP/WithGRPC/Domain...)"
        user_service -> contract "注册后端 Provider"
        contract -> appkit "实现 Provider v2（跨 module 回边）"
        contract -> adapters "构造后端实例"
        appkit -> bootstrap "Resolve 配置 + ServerProvider"
        appkit -> middleware "挂载 gin 链 / gRPC 拦截器链"
        bootstrap -> bconf "读取 BootstrapConfig"
        bootstrap -> bconfig "读取远程配置源"
        bootstrap -> spi_core "返回 transport.Server 实现"
        middleware -> spi_core "依赖 ratelimit / metrics / berrors 契约"
        adapters -> spi_core "实现 Server / Broker / Cache / Discovery / Codec"
        middleware -> otel_backend "span 与 metrics 导出（经 metrics/otel）"
        bconfig -> etcd_nacos "watch 配置"
        adapters -> etcd_nacos "服务注册（registry/*）"
        adapters -> mq "读写队列（broker/*、transport/{kafka,rabbitmq,rocketmq,redis}）"
    }

    views {

        systemLandscape "landscape" {
            include *
            autoLayout
        }

        systemContext "services_view" user_service {
            include *
            autoLayout
        }

        container "modules_view" bald {
            include *
            autoLayout
        }

        theme default
    }
}
