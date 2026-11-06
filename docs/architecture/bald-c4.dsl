// bald 架构模型（现状视图）— Structurizr DSL
// 校验方式：用官方 structurizr-dsl 6.2.4 解析器 parse + DOTExporter 导出，再用 graphviz 渲染
//   （见 docs/architecture/README.md 的"校验工具与命令"一节）；structurizr-cli 未发布到 Maven Central，故用库替代 CLI
// 证据日期：2026-11-01，main @ ca981b1

workspace "bald" "bald Go 多模块框架 — 当前架构" {

    model {

        // === People ===
        svc_dev = person "业务服务开发者" "用 appkit/std 装配一个可运行服务，或在 biz/ 域包里写业务逻辑"
        fw_maint = person "框架维护者" "维护 SPI、Provider、codegen 与多模块发布"

        // === External systems ===
        user_service = softwareSystem "业务服务进程" "消费方：_example/bald（github.com/kalandramo/bald/example/bald）与 bald-admin"
        etcd_nacos = softwareSystem "配置/注册中心" "etcd / consul / nacos / kubernetes，由 bconfig 与 registry 适配器对接"
        mq = softwareSystem "消息中间件" "Kafka / RabbitMQ / RocketMQ / Redis，由 broker 适配器对接"
        otel_backend = softwareSystem "OTLP 后端" "trace/metrics 导出目标，经 metrics/otel 与 contrib/observability-otlp"

        // === The system ===
        bald = softwareSystem "bald 框架仓库" "SPI + 适配器 + 声明式装配 + 代码生成（93 个 go.mod）" {

            spi_core = container "spi_core" "SPI 契约模块 — 只定义接口。transport/server.go:14-22 = Start/Stop/Endpoint；broker/broker.go:9-33 = Broker；cache/cache.go:28-69；registry/registry.go:62-75；metrics/metrics.go:15-19" "Go module"
            bconf = container "bconf" "bconf 配置契约 — protobuf 生成的强类型契约（bconf/proto → bconf/gen）：bootstrapv1.BootstrapConfig + appspec/v1" "Go module"
            bconfig = container "bconfig" "bconfig 配置源抽象 — Reader（bconfig/bconfig.go:17）/ Watcher（:42）/ Decoder（:78）+ file/env/etcd/consul/nacos/vault/apollo" "Go module"
            bootstrap = container "bootstrap" "bootstrap 装配桥 — 分层合并内核（bootstrap/config/config.go:1-17，优先级自高至低：flag、env、file、contract source、remote bridge）+ ServerProvider 工厂（bootstrap/server.go:25,89,137）+ 远程配置驱动" "Go module"
            appkit = container "appkit" "pkg/appkit 组合层 — v2 Provider Plan/Build + Segments（pkg/appkit/provider.go:1-24）、Run/stopAll 生命周期（pkg/appkit/appkit.go:561-703,715-750）、std 标准装配" "根 module 内包"
            middleware = container "middleware" "pkg/middleware 横切层 — bundle 链序固化：Gin() / GRPCInterceptors()（pkg/middleware/bundle/bundle.go:171-211,224-241）" "根 module 内包"
            adapters = container "adapters" "适配器模块 — transport/*(19) broker/*(4) cache/*(3) registry/*(4) log/*(5) metrics/*(3) encoding/*(12) ratelimit/*(3) circuitbreaker/*(4) oss ai workflow cobramcp" "Go module × N"
            contract = container "contract" "*/contract Provider 注册包 — 适配器接入 appkit v2 Provider 面的唯一接缝，如 transport/gateway/contract/provider.go:24、registry/etcd/contract/provider.go:16" "适配器 module 内子包 × 30"
            codegen = container "codegen" "cmd/bald + internal/codegen — bald gen app|domain|proto|store：从 appspec.json 产出 owned/seeded 文件树" "根 module"
            contrib = container "contrib" "contrib 扩展 — store-gorm/mongo、audit-*、authn-jwt、authz-casbin、config-nacos、observability-otlp、database" "Go module"
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
        middleware -> otel_backend "span/metrics 经全局 otel 出口（导出器由 contrib/observability-otlp 装配）"
        bconfig -> etcd_nacos "watch 配置"
        adapters -> etcd_nacos "服务注册（registry/*）"
        adapters -> mq "读写队列（broker/*、transport/{kafka,rabbitmq,rocketmq,redis}）"
    }

    views {

        systemLandscape "landscape" {
            include *
            autoLayout
        }

        systemContext user_service "services_view" {
            include *
            autoLayout
        }

        container bald "modules_view" {
            include *
            autoLayout
        }

        theme default
    }
}
