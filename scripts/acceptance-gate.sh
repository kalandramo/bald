#!/usr/bin/env bash
# 声明式装配验收闸门（风险登记册 §8 最后半条「acceptance 没有任何 CI 作业调用它」的收口）。
#
# ## 为什么不能把 `go test -run <正则>` 直接接进 CI
#
# `-run` 匹配到 0 个测试时**返回 0**：用例被改名、被删、正则写错，作业照样全绿。
# 本仓库已经被这类静默失效咬过两次（§7.1 的「不可达即 Skip」、§8 的 per-command
# `dir` 被 go-task 忽略），所以这里把「清单」与「执行」钉在一起：
#   1. `go test -list <正则>` 取出这一腿**当前真实命中**的顶层测试名；命中为空即红；
#   2. `go test -v -count=1` 跑同一正则，逐条断言日志里出现 `^--- PASS: <名字> (`；
#      Skip、改名、没链上都会落到这里。
# 判据来自 `-list` 而不是手写清单，因此增删用例不需要维护第二份真相；口径与
# ci.yml 的 integration 作业同一条。
#
# ## scope
#
#   all（默认，本地）：全部腿，含 `_example/bald`。
#   ci              ：只跑标 ci 的腿。两条 `no` 的各自理由：
#                   - `_example/bald` 的两条计划腿要编译 `api/gen` 的 proto 产物，
#                     而它已 gitignore——干净检出里连示例 module 都编译不了，本脚本
#                     不装 buf（装一遍工具链是专职作业的活）。**这不等于「这条腿没有
#                     远端证据」**：ci.yml 的 `example` 作业按同一把 pin 的 buf
#                     （scripts/install-buf.sh）生成产物后跑三态构建，两条
#                     `TestPlan_ReferenceProject`（默认态 + grpcgw 态）都在它的判据
#                     清单里逐条断言顶层 `--- PASS`（登记册 §7.1）。留 `no` 是为了
#                     不在两条作业里各装一遍 buf。
#                   - `bconf` 的 `TestGeneratedMatchesProto` 判的是「buf +
#                     protoc-gen-go 重出的产物与入库字节一致」，**这个字节判据绑
#                     工具链**：进字节的排版变量是**编译 protoc-gen-go 用的那把 Go**
#                     （go1.26.5 给中文注释的 `//` 补一个空格，go1.27.1/go1.27.3 与
#                     入库零差异；BSR 容器里的同名插件补空格也是同一成因），buf 的
#                     版本号实测对这一处中性（1.50.0 与 1.57.2 配同一把插件都零差
#                     异）。三把 pin 都成文并机器判了：`bconf/buf.version`（这条腿
#                     唯一还读 PATH 的一轴）、插件 module 版本比 `bconf/go.mod` 的
#                     protobuf **现编**出的那把（再比产物头部的 protoc-gen-go 版本行）、
#                     编译该插件的 Go major.minor 比 `bconf/protoc-gen-go.goversion`。
#                     后两轴读 PATH 上已装插件时，「那把是哪年装的」能冒充判据通过或
#                     判红（登记册 §7.9 前四轮），现编之后 PATH 里那把不再参与。
#                     本作业**不装 buf**：它的工具链前提与 `_example` 那两条不同，
#                     已经由专职作业 `proto-contract` 满足并取证（官方 buf 发行
#                     二进制按 `bconf/buf.sha256` 校验 + 判据自己现编插件 +
#                     「没有顶层 PASS 即红」），见登记册 §7.9 第五/六轮。本表留 `no`
#                     是为了不在两条作业里各装一遍工具链——不是「这条腿没有远端证据」。
#
# 表列以 TAB 分隔，**tags 放最后**（TAB 属 IFS 空白，连续 TAB 会被折叠成一个分隔符，
# 中间列留空就会串位；可空列只能放在行尾）。
#   <ci|no>	<模块目录>	<包>	<-run 正则>	<go build tags，可空>
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
export GOWORK=off

scope=${1:-all}
case "$scope" in
all | ci) ;;
*)
  echo "usage: $0 [all|ci]" >&2
  exit 2
  ;;
esac

# transport/asynq（§7.19）与 contrib/database/gorm（§7.20）两条腿的后端都在**进程内**
# ——前者是 miniredis，后者是纯 Go 的 go_sqlite 驱动落 t.TempDir() 的文件——都不需要
# 服务容器，所以它们能直接落进 scope=ci。与 nacos/consul 那类要真服务的腿不同。
# registry/nacos 那条（§7.22）同理但机制不同：它把 nacos 换成进程内的 HTTP 桩，判的是
# SDK 构造期**同步发出**的那个登录请求（server_addrs/username/password 三行的直读口），
# 所以也不需要服务容器；同一个包另外六条 `TestMappingBackend_*` 要真后端，不进本表，
# 由 ci.yml 的 integration 作业点名（docscheck 认那条路径）。
# contrib/database/mongodb 那条（§7.23）的离线桩同样是进程内的——`net.Listen` 出来的
# 「接受 TCP 但永不回 MongoDB 握手」的假线。八行里 uri / server_selection_timeout /
# connect_timeout 三行，加上 database 的「缺席语义」，共四条腿能离线读；database 取值、
# credentials 两态、heartbeat、max_conn_idle 共五条腿要真后端，走 ci.yml 里 mongo:7
# 服务点名，不进本表；timeout_seconds 一行**只有** `contract_test.go` 的长度判据——上游把
# 每个操作的截止期写死成 10s，恰好掩掉契约值，运行期两侧都判不到（理由写在该包
# mapping_backend_test.go 文件头）。
# broker/rocketmq 那条（§7.24）的离线桩也是进程内的：v2 客户端把每个请求都发成本机监听
# 端口上的明文 JSON 帧，所以八行（name_servers/name_server_url/access_key/secret_key/
# security_token/group_name/namespace/instance_name/retry_count/topic）**全部**能在不打
# 真后端的情况下读出——路由查询（105）的 extFields.topic、心跳（34）body 的 clientID、
# 发送（10）的 producerGroup、ACL 签名逐条值判。停机两条腿（Unsubscribe/Disconnect 关掉
# consumer）判的是同一根线上的「停机前流水计数已 ≥2（正向对照）+ 停机后睡满 4s 窗口新增为
# 0（absence）+ 假 broker 侧连接归零」，所以窗口不是固定 sleep 蒙过去，而是有对照实证的
# 「确实在长 → 停之后确实停了」。没有要真后端的腿，所以本包不需要进 ci.yml 的 integration
# 点名。`./v2 TestLoggerLevel` 那一条是本模块第五处生产修复（SDK 日志全局只注入一次、级别
# 改原子）的确定性腿：它判「级别仍然生效，而且生效在被注入的那一个对象上」，不需要 -race
# 就判得动；同包另一条 TestLoggerConcurrent 只有 -race 才判得动，而本模块因上游 `close`
# 竞争在 .github/race-skip.txt 里，把它写进本表只会得到一条永不判红的腿。
# log/aliyun 那两条（§7.25）的离线桩也是进程内的：SDK 的 producer 把每条日志 POST 到
# `<scheme>://<project>.<endpoint host>/logstores/<logstore>`，而它的 Transport 是
# http.DefaultTransport 的 Clone()，换掉进程默认值就能把任意 project 域名拨到本机监听——
# 签名（V1 HMAC-SHA1）、LZ4 块压缩与 URL 拼装全走 SDK 真码，只有名字解析被改道。六行因此
# 全部离线可读：endpoint 读「拨到假线端口 + scheme 决定首包是 HTTP 请求行还是 TLS 记录」，
# project/logstore 读 host 前缀与 path，access_key 读 Authorization 的签名主体，
# access_secret 由测试独立复算 V1 签名判定（另配一条错密钥对照保证判据有分辨力），
# security_token 读 x-acs-security-token（留空则该头缺席）。`./contract TestMappingOffline_`
# 是本包主腿；`./ TestNewAliyunLogger_Rejects...|TestDefaultOptions...` 那一条点名五项必给
# 的构造期 fail-fast 与「options 层不许有占位默认」这条不变量——两者都是本轮的生产改动，
# 静默失效时最容易被读成「配置照旧能跑」。本模块在 .github/race-skip.txt 里（上游
# ProducerMonitor.wg 竞争），但这两条腿不依赖 -race，写进本表有意义。
# metrics/{prometheus,otel,datadog}/contract 三条腿（§7.27）判的是新增的顶层
# instrumentation 段：三族的观测面都在进程内，都不需要服务容器——
#   - prometheus：client_golang 的 Registry.Gather() 是纯内存的，namespace/subsystem
#     读族名拼接，use_default_registry 读族落在**哪个 Gatherer**；
#   - otel：两把假收集器——httptest 当 OTLP/HTTP 落点（POST /v1/metrics，正文默认
#     gzip，判前先探 magic），真 gRPC MetricsService 桩当 OTLP/gRPC 落点（能解析出
#     ExportMetricsServiceRequest 就同时证明了「走 gRPC」和「明文」）；insecure=false
#     那一格只能看首字节（ClientHello 0x16 0x03），而落点不回话时 cleanup 会按
#     Provider.Close 的 10s 上限全额等待末次上报，本表这条因此天然带 10s；
#   - datadog：本机 UDP 监听读 statsd 线格式，buffer_size 是真·单报文上界，
#     sample_rate 实测只作用 Histogram 一条路径（Counter/Gauge/GaugeAdd 走客户端
#     聚合，聚合器在 flush 时把 rate 硬编成 1），这条不对称写在 proto 注释和测试
#     文件头里，不藏。
# 三行前缀 TestMappingOffline_ 各自命中该包的映射行全集；otel/datadog 的 AppProvider
# 五条腿（Segments/Apply/Plan/Build/缺席 fail-fast）由 CI 的 test 作业整包跑，不进本表
# ——它们没有时序窗口，被 -short 影响不到，重复点名只会得到两条同证据的腿。
# log/tencent 两条腿（§7.26）判的是 tencent 段四行映射 + cleanup + 凭证口径。假线姿势
# 与 aliyun **不同形**，照抄会得到假绿：CLS 客户端自己 new 一个 *http.Transport，
# patch http.DefaultTransport 拨不动它——endpoint 是唯一能把请求改道到进程内的入口，
# 所以假线就是 endpoint 本身（http://127.0.0.1:<port>），签名、lz4 压缩、URL 拼装仍走
# SDK 真码。逐行读法：endpoint 读假线 host 与 scheme（https 腿配一条反面判据——默认
# 客户端必因自签证书 x509 失败，而 SDK 的请求仍然到达，因此「连上了」不等于「验了证书」，
# 上游 TLS 配置硬编 InsecureSkipVerify 这件事被实测钉住）；topic_id 读 query 的
# topic_id；access_key 读 Authorization 的 q-ak；access_secret 由测试独立复算 CLS V1
# 签名（另配错密钥对照保证分辨力）；正文 lz4 解回 protobuf LogGroupList 逐字段判；
# cleanup 判「返回时假线上已有请求」，其分辨力来自 producer 默认 LingerMs=2000——
# 把 Close 摘掉的那刻起这条判据就红。`TestSdkAssumption_EmptyCredentialsSendUnsigned`
# 绕过 bald 的构造期校验直接喂 SDK，钉住「凭证升为必给」的前提：SDK 的 validateOptions
# 里那段凭证校验是注释掉的，Send 只在 SecretID 与 SecretKEY 都非空时才加 Authorization
# 头——空凭证得到的是完全未签名的请求，不是「带空身份的签名」（这条与 aliyun 不同形，
# 那里空 AK 仍签得出 "LOG :<sig>"，所以不能类推、只能各判一次）。`.\t` 那条点名五项
# 必给的构造期 fail-fast 与「options 层不许有占位默认」。
# transport/{tcp,websocket,http3}/contract 三条腿（§7.28 方向二·第一批）判的是
# 三个「契约有段、装配无门」的服务器面首次经 Provider 进计划。三族都离线可判
# （监听自己起，不需要服务容器），但读法各不相同，不能互相类推：
#   - tcp：帧格式 4 字节 LE uint32 类型 + 原样 payload，处理器以 nil Creator 注册
#     即拿到原始字节，因此这条判据不需要注册任何编解码器；tls 腿一律走 tlsDial
#     （有界握手）——实测无界 tls.Dial 在「tls 守卫被摘掉」的变异下会挂到包级
#     -timeout 得到一条超时判红，换成有界握手后同一变异点名 4 条 FAIL，真因（明文
#     端口不回 ServerHello）在消息里；
#   - websocket：path 在 NewServer 里就进了 mux 注册表，所以「契约 path 顶上默认
#     /ws」只能靠**真握手**判——正例打契约 path，反例打 /ws 必须 404；空 path 段的
#     反直觉读法（不追加 WithPath）由「默认 /ws 仍可握手」这条线上腿证；
#   - http3：QUIC 必须有 TLS，本模块自己的 `(*Server).init`（transport/http3/
#     server.go）硬编 :443 并在无 tlsConf 时自签，所以 addr 必填的判据
#     文案里带着 :443（表内 addrguard 变异只红 2 条，正是这两条 fail-fast）；证书
#     行的分辨力来自「换一张无关根必拒」，自签默认过不了契约根的校验；mTLS 行的
#     失败原因是远端 `tls: certificate required`，不是本地超时。
# tcp 那条 TestAppProviderEndToEnd_ 是**本批唯一的真进程端到端腿**：契约 yaml →
# appkit.Serve → 段命中 AppProvider → 构造 → Start → 线上往返，外加反证「段名写成
# server.tpc 时 Serve 必须失败」。入口只能是 Serve 不是 NewApp+Run——assemble 只挂在
# Serve 上，NewApp+Run 会跳过整个段驱动装配（一声不响地把 Provider 丢在计划外）。
# 同一前缀下还有 §7.27 收尾的两条 instrumentation 取用腿（InstrumentationSegment
# ReachesScrapeEndpoint / MetricsSegmentAbsentFailsServe）：段产物经域 Wire 递到
# after-build，真帧计数出现在独立 metricshttp 监听面上；反证腿判段缺席时消费侧就失败。
# 它们不改本行的 -run（前缀相同），但让这行的判据数从「工厂形状」扩到「取用链路」。
# websocket/http3 的 AppProvider 五条骨架腿（Segments/Apply/Plan/Build/缺席语义）由
# CI 的 test 作业整包跑，不进本表——它们没有时序窗口，重复点名只会得到两条同证据的腿。
# transport/{graphql,webrtc,mcp}/contract 三条腿（§7.28 方向二·第二批）同判「契约有段、
# 装配无门」首次开门，三族的观测面各不相同：
#   - graphql：真 HTTP 往返——契约 path 上打查询、tls 腿证书 CN 与 mTLS 拒无证书、
#     业务中间件响应头；
#   - webrtc：信令 HTTP 状态机——POST {} 得 400 + body「invalid offer」是业务指纹
#     （变异摘掉 tls 时明文判据正是要**没有**这个 body 才红），GET 405 / 预检 204+CORS /
#     未注册 404 各判一次；Endpoint() 把 path 嵌进 URL 且 tls 下必须翻 https（缺陷①的
#     判据），Use 在装配期（NewServer 后）追加的中间件要靠 Start 重建链才上线（缺陷②），
#     这两条各有一条线上腿与一条源码变异（W8/W9）互相钉住；
#   - mcp：真 MCP 协议往返——initialize 响应里的 ServerInfo 判 name/version（空字段
#     回落实现默认而非置空），tools/call 判 afterBuild 注册的工具，Endpoint() 恒空是
#     STDIO 的判别形状（契约强制 HTTP：STDIO 不绑端口，写了 address 就必须听得见）。
# 三族都有 TestAppProviderEndToEnd_（契约 yaml → appkit.Serve → 段命中 → 真进程往返 +
# StrictContract 拼写反证），与 tcp 同口径进表；骨架腿仍由 CI 的 test 作业整包跑。
# transport/{kafka,redis,rocketmq,rabbitmq}/contract 四条腿（§7.28 方向二·第三批）同判
# 「契约有 MQ 段、装配无门」首次开门，但四族的离线可判面**互不相同**，不能互相类推：
#   - kafka：broker 的 Connect 只翻标志位，因此 Start 离线就绿；订阅 flush 是「懒成功」
#     （生产者协程后台重试），所以订阅者两腿判的是登记与 live 路径不报错；
#   - redis：pubsub 的 Connect 是真拨号 + PING 探针，绿 Start 必须有个会回话的对端——
#     进程内起了个只回 PING/SUBSCRIBE/UNSUBSCRIBE 的最小 RESP 应答器（线格式仍由 redigo
#     自己解析，不伪造协议以外的东西）。端点剥凭证那条同时钉住「URL 里的 user:pass 不许
#     泄进就绪面」；另配一条拒连端口腿，判探针红时端点保持空；
#   - rocketmq：Connect 也只翻标志位 ⇒ 无订阅者的 Start 离线绿；但订阅 flush 走 SDK
#     PushConsumer 的同步启动（拉路由要真 NameServer），离线必红——两条订阅腿因此判的是
#     **失败形状**（订阅确实递进了 SDK、失败后 started 不落真）。driver_type 的 proto
#     注释「默认 v5」与实现（恒 v2）是真漂移，收口成 fail-fast + Broker.Name()=="rocketmqV2"
#     守卫腿，而不是把注释改绿；
#   - rabbitmq：Connect 是完整 AMQP 0-9-1 握手，进程内假后端不可信（伪造帧序只会把
#     「握手过没过」变成自证），所以四行映射的运行期判据是拒连端口上的**拨号目标**——
#     错误文案里必须出现契约那个 host:port（同时判 Init 的 amqp:// 规范化真写回了
#     options.Addrs、以及 addrs[1:] 在该后端是惰的）。端点的 started 闸门与 guest:guest
#     脱敏在包内注入 started 判（表里单独一行 transport/rabbitmq .），因为离线跑不到
#     Start 成功；「Start 后端点变描述性」「订阅真挂上队列」属真集群判据，本轮不拿假握手冒充。
# 四族都有 TestAppProviderEndToEnd_（契约 yaml → Serve → 段命中 + StrictContract 拼写反证
# rmq/rds/kfk/rbmq）；redis/kafka/rocketmq 的真进程腿读到「Start 后的描述性端点」，
# rabbitmq 那条读到的是「Build 的 fail-fast 一路掀掉 Serve」——差别就是上面那条离线边界。
# 四族骨架腿（Segments/Apply/Plan/Build/三态跳过）仍由 CI 的 test 作业整包跑，不重复点名。
rows() {
  printf '%s\n' \
    $'ci\tpkg\t./appkit\tTestPlan_(NoSideEffects|OrderInvariant|SegmentCoverage|Conflict|DomainRoutes|CustomRoutesWarning|GlobalMeterProvider)|TestRouteShapeConflict\t' \
    $'ci\tpkg\t./appkit\tTestTeardownOrder_|TestProviderPlan_StdTeardownOrder|TestComponent_StartFailureRollback|TestMountComponent_MountFailureRollback\t' \
    $'ci\tpkg\t./appkit\tTestDrift\t' \
    $'ci\tpkg\t./appkit\tTestPlanCache_|TestVariantProjection_|TestVariantEffects_|TestOption_OnRoleScoping|TestDoctor_\t' \
    $'ci\tpkg\t./appkit\tTestAPI_\t' \
    $'ci\tcli\t./internal/codegen\tTestGen_(Deterministic|CheckClean)|TestGenDomain_(Bind|Proto|NoRoutes|CheckDoesNotRunProtoStage)|TestGenApp_Variants|TestTree_Variants|TestTree_CustomWiring|TestPruneStaleOwned|TestCheckTree_\t' \
    $'ci\tregistry/etcd\t.\tTestRetreatDelay_|TestRetreatPolicy_|TestHeartBeatRetreat\t' \
    $'ci\tregistry/etcd\t./contract\tTestRetreat_|TestProvider_BackoffSection|TestProvider_ContractSections|TestProvider_MaxRetrySection|TestProvider_AuthSection\t' \
    $'ci\tregistry/consul\t./contract\tTestProvider_RegisterBody|TestProvider_CheckFlags|TestProvider_AbsentBools|TestProvider_AbsentIntervals|TestProvider_SchemeSection|TestProvider_DatacenterSection\t' \
    $'ci\ttransport/asynq\t./contract\tTestMapping_\t' \
    $'ci\tcontrib/database/gorm\t./contract\tTestMapping_|TestAllowedDriversTableShape\t' \
    $'ci\tregistry/nacos\t./contract\tTestMappingOffline_\t' \
    $'ci\tcontrib/database/mongodb\t./contract\tTestMappingOffline_\t' \
    $'ci\tbroker/rocketmq\t./contract\tTestMappingOffline_\t' \
    $'ci\tbroker/rocketmq\t./v2\tTestLoggerLevelReachesInjectedLogger\t' \
    $'ci\tlog/aliyun\t./contract\tTestMappingOffline_\t' \
    $'ci\tlog/aliyun\t.\tTestNewAliyunLogger_RejectsIncompleteOptions|TestDefaultOptionsHasNoPlaceholderTarget\t' \
    $'ci\tlog/tencent\t./contract\tTestMappingOffline_|TestSdkAssumption_\t' \
    $'ci\tlog/tencent\t.\tTestNewTencentLoggerRejectsIncompleteOptions|TestDefaultOptionsHasNoDefaults\t' \
    $'ci\tmetrics/prometheus\t./contract\tTestMappingOffline_|TestProvider_RejectsNilSection\t' \
    $'ci\tmetrics/otel\t./contract\tTestMappingOffline_\t' \
    $'ci\tmetrics/datadog\t./contract\tTestMappingOffline_\t' \
    $'ci\ttransport/tcp\t./contract\tTestMappingOffline_|TestAppProviderEndToEnd_\t' \
    $'ci\ttransport/websocket\t./contract\tTestMappingOffline_\t' \
    $'ci\ttransport/http3\t./contract\tTestMappingOffline_\t' \
    $'ci\ttransport/graphql\t./contract\tTestMappingOffline_|TestAppProviderEndToEnd_\t' \
    $'ci\ttransport/webrtc\t./contract\tTestMappingOffline_|TestAppProviderEndToEnd_\t' \
    $'ci\ttransport/mcp\t./contract\tTestMappingOffline_|TestAppProviderEndToEnd_\t' \
    $'ci\ttransport/kafka\t./contract\tTestMappingOffline_|TestAppProviderEndToEnd_\t' \
    $'ci\ttransport/redis\t./contract\tTestMappingOffline_|TestAppProviderEndToEnd_\t' \
    $'ci\ttransport/rocketmq\t./contract\tTestMappingOffline_|TestAppProviderEndToEnd_\t' \
    $'ci\ttransport/rabbitmq\t./contract\tTestMappingOffline_|TestAppProviderEndToEnd_\t' \
    $'ci\ttransport/rabbitmq\t.\tTestEndpointAfterStartStripsCredentials|TestSanitizeEndpoint\t' \
    $'ci\tbconf\t.\tTestUnmarshalNestedBackoffSection|TestUnmarshalAbsentBackoffStaysNil|TestUnmarshalDurationSpellings|TestCoerceDurationFormat\t' \
    $'no\tbconf\t.\tTestGeneratedMatchesProto\t' \
    $'no\t_example/bald\t./internal/bootstrap\tTestPlan_ReferenceProject\t' \
    $'no\t_example/bald\t./internal/bootstrap\tTestPlan_ReferenceProject\tgrpcgw'
}

fail=0
ran=0
checks=0
while IFS=$(printf '\t') read -r in_ci dir pkg regex tags; do
  [ -n "${in_ci:-}" ] || continue
  if [ "$scope" = ci ] && [ "$in_ci" != ci ]; then
    echo "SKIP（scope=ci 不含这条腿，理由见表头）: $dir $pkg $regex"
    continue
  fi
  ran=$((ran + 1))
  echo "=== acceptance: $dir $pkg${tags:+ -tags $tags} -run $regex"

  # bash 3.2（macOS 默认）在 set -u 下展开空数组会报 unbound，故用 ${arr[@]+...}。
  tagargs=()
  [ -n "${tags:-}" ] && tagargs=(-tags "$tags")

  log=$(mktemp)
  # 1) 清单：正则当前命中哪些顶层测试。空清单 = 名字漂了，闸门必须响。
  if ! (cd "$dir" && go test -list "$regex" ${tagargs[@]+"${tagargs[@]}"} "$pkg") >"$log" 2>&1; then
    echo "::error::go test -list 失败（$dir $pkg）"
    cat "$log"
    fail=$((fail + 1))
    rm -f "$log"
    continue
  fi
  names=$(grep -E '^Test[A-Za-z0-9_]+$' "$log" | sort -u || true)
  rm -f "$log"
  if [ -z "$names" ]; then
    echo "::error::-run 正则在 $dir $pkg 命中 0 个测试——用例改名或被删时 -run 照样返回 0，这正是本闸门存在的理由"
    fail=$((fail + 1))
    continue
  fi
  echo "    命中：${names//$'\n'/ }"

  # 2) 执行并逐条验活。
  log=$(mktemp)
  if ! (cd "$dir" && go test -count=1 -timeout 10m -v -run "$regex" ${tagargs[@]+"${tagargs[@]}"} "$pkg") >"$log" 2>&1; then
    echo "::error::测试腿失败（$dir $pkg）"
    # 打原文而不是挑行：t.Fatalf 的清单缩进在两空格，按 `^--- ` 过滤会把根因整段吞掉。
    tail -40 "$log"
    fail=$((fail + 1))
    rm -f "$log"
    continue
  fi
  for name in $names; do
    checks=$((checks + 1))
    if grep -qE "^--- PASS: ${name} \(" "$log"; then
      echo "    PASS $name"
    else
      echo "::error::判据 $name 没有顶层 PASS 记录（Skip / 改名 / 未编译都会落到这里）"
      grep -E "^(    )?(--- )?(SKIP|FAIL): ${name}" "$log" | head -5
      fail=$((fail + 1))
    fi
  done
  rm -f "$log"
done <<EOF
$(rows)
EOF

if [ "$ran" = 0 ]; then
  echo "::error::一腿都没跑（scope=$scope，表为空或谓词被改坏）——闸门静默失效"
  exit 1
fi
if [ "$fail" != 0 ]; then
  echo "acceptance-gate: $ran 条腿 / $checks 条判据，$fail 条未通过"
  exit 1
fi
echo "acceptance-gate: scope=$scope 的 $ran 条腿、$checks 条判据全部通过"
