# 变更日志

本文件由 `bald release changelog` 从提交历史生成：每个模块一节，版本按发布时间倒序。
条目取自该版本区间内的提交标题（Conventional Commits），不要手工调整顺序。
发布流程见 [多模块发布与变更日志](docs/devel/zh-CN/多模块发布与变更日志.md)。

## 2026-11-03

### pkg v0.1.0

- `96582bfb` refactor(modules): pkg/ 独立成 module，require 聚合根的适配器 22→0（§3.1）
- `10741ca8` feat(gateway): 把服务发现接到装配上，并在真注册中心上验活
- `89fbb20f` feat(middleware): gRPC 链补限流与超时两层（§6.2）
- `dd2123f4` refactor: 转码面收归 gateway 段 Provider，断开根模块与适配器的模块环
- `cddac6fd` test(appkit): 热更漂移断言改为等落账，不再抢读一次
- `ffa15ffc` test(bconf): 分配剖面守卫按实测重定基线，并写明 -race 档
- `92de7dc6` fix(bundle,codegen): 认证姿态未决定不再静默开放
- `8afe0f3e` fix(appkit): 停机各阶段预算派生自唯一起停入口，反注册失败不再被吞
- `2e9b2d2c` fix(bconf,tls): 契约驱动 TLS 覆盖 gRPC，收敛 gRPC 选项注入路径
- `682739d8` fix(appkit): 测试等启动完成再取消，避免慢机器误报
- `0940278a` test(appkit): 构建约束用例不再依赖宿主平台
- `77caa1f5` fix(store)!: Update 注入租户/数据范围隔离谓词 **(breaking)**
- `fe9fdeac` fix(appkit): 路由冲突检查识别段内通配
- `07904069` style: 清理全仓 gofmt 漂移
- `3ad6cdd0` fix(appkit): 启动失败在日志后端就绪前兜底写 stderr
- `bf1f8674` feat: P1 消费侧复用能力落地
- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `c2e57346` docs: 删除历史路线文档，增加文档路径一致性检查
- `15d09e9f` refactor(appkit,codegen): 按关注点拆分三个大文件
- `b12a8ac9` test(fieldmaskutil): 补 nested_mask 关键路径覆盖
- `43ed27e2` test(ci): 增加 -race 门禁并修复并发缺陷
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
- `f69775d5` refactor(appkit)!: 移除 AdaptLegacy，Provider 只保留 v2 语义 **(breaking)**
- `9a8b1866` feat(codegen): V3——变体双产物生成与示例迁移
- `321a26a7` feat(appkit): Q7 V2——变体声明化为装配增量，命中即普通计划实体
- `271b3247` feat(appspec): Q7 V1——变体生效条件改用 go/build/constraint 表达式
- `3da2cfd4` feat(appkit): Q7 计划缓存对账纳入构建变体，命中态变化不再隐身
- `ae39366b` feat(appspec): Q5 D1——gen domain 默认生成 REST 绑定，前置缺失即编排 proto 阶段
- `79416489` refactor(appkit): 命中变体并入 ambient 身份空间——与 WithAmbient 同套重名裁决
- `dc3f140b` feat(appkit): 变体声明恒进计划——不带 tag 也能审计，命中另产 ambient（Q7 深化）
- `7ea0c5a9` feat(appspec): AppSpec.variants 进声明——生成物按实际 build tags 投影 ambient（Q7 首片）
- `a7e1f936` feat(appkit): doctor 基线对比纳入路由清单
- `f13eec7d` feat(appkit): 路由形态校验前置到计划期
- `562f300d` feat(appkit): 路由冲突检测对齐 gin 注册语义
- `fa1980fa` test(appkit): 验收门可执行化——参考工程计划断言 + 手工排序表守卫 + task acceptance
- `57120789` feat(codegen): --rest-bind 同批产出 typed 路由清单——注解派生路由进计划（Q5 可见性）
- `636dd8cb` feat(appkit): Domain.RouteTable——REST 路由清单进计划，跨域冲突计划期报错
- `8bb020f7` fix(appkit): WithAmbient 重名裁决——同 Detail 合并、异 Detail 计划期报错
- `b0163183` feat(appkit): imperative 段补身份端口与审计 sink
- `bbde6578` feat(appkit): --plan 显式服务器能力面（serverCapability）
- `685927fa` refactor(appkit): std logger/audit 改契约派生——逃逸舱只剩 server
- `52e81b5f` feat(appkit): 计划缓存对比单列诊断增删
- `39bb5de2` fix(appkit): doctor 的「计划未变」限定到装配声明范围
- `b1b6a9b0` feat(appkit): 计划 imperative 段补上服务器工厂与热更回调
- `f98b5903` feat(appkit): 生命周期钩子进入计划 imperative 段
- `4662daf4` feat(appkit): 计划差异逐条列出命令式槽位增删
- `424a06d7` fix(appkit): plan cache 对比不把槽位变化谎报成 plan unchanged
- `3cfb9a6e` fix(appkit): 计划中消失的可协调节点报重启，不静默忘记
- `2b4a2403` fix(appkit): config/server 段 Provider 声明 reconcile 计划期报错
- `aaa70890` test(appkit): 钉住 kitOpts 重建的其余声明面透传
- `4b6aad06` feat(appkit): biz.<path> 进段汇总——Q8 节点的读取在 segments 视图可见
- `3d9636ca` feat(appkit): 命令式槽位进计划——effect/keyWatch/reconcile 不再隐身
- `9fbd6988` fix(appkit): Serve 路径合流显式 ConfigLayers——契约源层居首
- `1c8b9535` fix(appkit): 装配路径补齐 AfterStop/机制超时/Identity/Auditor 透传
- `ee8862dc` fix(appkit): 装配路径恢复用户 OnConfigChange 回调
- `c43638e3` feat(appkit): biz.<path> 段进入可协调节点期望态——L4 补上域自有配置收敛（Q8）
- `3f86b000` feat(appkit): WithAmbient——build tag 分叉进计划 ambient 警告（Q7 短期形态）
- `35dc13c7` fix(appkit): ApplyNode 域能从 App 拿到 WithHTTP 声明的 handler
- `cdd5b392` feat(appkit): Domain.ApplyNode——声明节点按计划序可执行，迁移域逃逸舱归零
- `becd49e7` feat(appkit): doctor 报告域迁移进度（§4.6 量化口径）+ 逃逸舱预算落地
- `39a16d80` fix(appkit): 声明域节点的段路径须是契约真实字段
- `d342d0c6` feat(appkit): Domain.Reconcile——声明域可协调节点接上热更收敛
- `b806a15c` feat(appkit): 计划缓存——启动记基线 + doctor 只读对比 + WithRole 角色入计划
- `dd17f87a` fix(appcli): doctor 子命令放行框架 flag——--config 由 appkit 自扫描
- `aa6c734e` feat(appkit): Domain v2 Declare——域拓扑进计划，示例 Opaque 5→3 达标
- `14762bed` feat(appkit): 装配诊断 doctor——逃逸舱集中报告 + 声明式等级口径
- `a8fb2f49` feat(appkit): M5 公开 API 收敛——删 App.Resource(string)、白名单校真、Opaque 预算纳管
- `d8070609` feat(appkit): --plan 文本视图显示非默认重载策略（reload=reconcile/restart）
- `d2e519cd` feat(appkit): M3 节点级重载策略与 Drift——配置变更重算计划 + 可协调节点重建 + 热更零值修复
- `f76678ce` feat(appkit): M2 类型化资源句柄——Key[T] + Provider 导出 Key + I9/I10 守卫
- `609cc9fd` feat(appkit): M1 计划驱动装配——Provider v2 双阶段 + 停机序数据化 + 三张手工排序表删除
- `5d1cb747` refactor: W2 日志壳集群清理——删 76 个零调用导出 Log* + 39 个包内壳非导出化 + 死代码与兼容别名（owner 批准破「不删导出名」纪律）
- `9e379a80` feat(appcli): CLI 机制层单轨——生成物 command.go 三行委托（缺口 #16）
- `843d3d2a` chore: 清理注释中的批次/排期/迁移痕迹——只留代码逻辑注释
- `6a658e12` ci: 基准冒烟入循环 + 分配剖面守卫测试 + 判据④计时独立 step + windows 周跑（批 6 收尾）
- `e4cf3e52` test(bench): 性能基准套件 15 项 + v0.24.0 对照实测 + 性能基准.md（批 5 W3）
- `bcd0f9b4` feat(codegen): 判据 ④ 端到端计时脚本 + 注释级陈旧引用清零（批 3 W4）
- `eedd2279` feat(appkit): 业务域一等概念 Domain（批 3 W2，判据 ① 框架侧）
- `4d91ec2c` refactor!(appkit,config): 职责边界落点——B6 两遍配置合并 + B7 能力面下沉（批 3 W1） **(breaking)**
- `97b7fa37` refactor!(appkit,codegen): AppKit 更名 App；codegen 模板重写为批 2 目标形态（批 2 W4） **(breaking)**
- `407a31a8` refactor!(appkit): 装配面收敛——双拼合并 + Resource(segment) + mech 下沉（批 2 W3） **(breaking)**
- `76d9da9f` refactor!(appkit): 段驱动 Provider 装配引擎替换 12 段手工流（批 2 W2b） **(breaking)**
- `3ae9a057` refactor!(appkit): 单一装配入口 Serve/MustServe/NewApp（批 2 W2a） **(breaking)**
- `786def85` refactor!(appkit): Option 接口化 + Decl 统一装配期声明（批 2 W1） **(breaking)**
- `9a083c6e` refactor(appkit,audit): 审计组装内化 pkg/audit.Compose；L3 段覆盖告警
- `b026ab37` refactor(appkit): 建立机制层子包 pkg/appkit/mech
- `8d55519d` refactor(bootstrap,appkit): 13 份装配期注册表收敛为泛型薄壳
- `bda91e75` feat(appkit): 暴露严格契约校验开关（WithStrictContract / StrictContract）
- `71a51c15` refactor(bootstrap)!: 配置源注册表改名 Registry→ConfigRegistry **(breaking)**
- `56cc116d` docs: 清理跨仓编号残留——Wave/T/M/P/D 系列共 540+ 处
- `1678235e` docs(metrics): 两个 metrics 包互相说明——消除「同名不同义」歧义
- `abb3461e` feat(appkit)!: 配置命名空间与 app.name 解耦——消除 env 前缀自指循环 **(breaking)**
- `3ebf85e8` fix: CORS 通配来源 + 凭证的组合在装配期 fail-fast（浏览器禁止的静默失效形态）
- `74f53b13` fix: 接通 server.http.middleware.* 五段（cors/recovery/request_id/logging/tracing/timeout）
- `cd2928e8` feat: 补 D7 —— authz 可选策略重载能力（ReloadableAuthorizer）
- `f04d90c1` fix: 接通 D5 —— server.http.rate_limit 契约段落地为限流中间件
- `19690cbb` feat(appkit,bootstrap): 补 server 协议域注入口 WithServerRegistry
- `9e2f4bbb` docs(appkit): 澄清四组可组合性原语与生命周期钩子的职责边界
- `f0d15afe` fix(appkit,cron,store): done 双关防护 + cron 秒级契约落地 + 数据范围语义钉住
- `ea1283dc` fix(store,codegen): 补齐数据范围策略的可逆原语与 T1 接线
- `ebddcf22` refactor(appkit): AuditProvider 接收资源容器，审计装配后于数据客户端
- `5e65fd4f` feat(appkit): 新增 WithExtraServerFunc，支持运行期构造附加服务器
- `2357d087` feat(appkit): 新增 WithGRPCOptions / WithGRPCUnaryFunc，支持运行期求值 gRPC 选项
- `7137488d` feat(appkit): FromBootstrap 补业务配置 flag 绑定通道（W2 前置）
- `3badb2d7` refactor(appkit)!: server 构造延后到 Run 期——消除装配时序倒置（W1） **(breaking)**
- `18723b24` fix(crudbridge)!: SimpleViewer 增显式 Platform 字段 + 中间件透传 claims.Platform **(breaking)**
- `2da3bd84` test(store): 补门面层与数据范围直测——覆盖率 79.8% → 87.5%
- `6490aeb5` feat(store)!: 统一租户隔离闸门 + 平台级身份豁免（修 ListWithPaging 暗雷） **(breaking)**
- `15bda01f` refactor(contrib)!: audit-store 更名为 audit-gorm，消除与 pkg/store 的语义歧义 **(breaking)**
- `15d83aaa` fix(store)!: 去 tenant_id 有损转换 + 平台级表隔离豁免（D20/D21） **(breaking)**
- `d546b19d` refactor(store): 收敛边界 9——translate 统一走 detectStrategy，消除 NoPaging 重复
- `b04dc788` fix(store)!: 收敛安全边界 1——Get/List/Count/Delete 自动注入隔离 **(breaking)**
- `b51218c6` fix(store): 收敛已知边界 2/6——Where.T 保留 Expr + 填充 CurrentSize
- `8921475a` docs(store): 修复设计文档遗留偏差——锚点刷新 + 注释对齐 + 边界补录
- `9171a96c` docs(store): Update 注释标注 rows 跨后端语义差异（审查 HIGH 修复）
- `d8c2753d` refactor(store)!: Update 幂等化——签名改 (int64, error)，0 行返回 (0, nil) **(breaking)**
- `9ca39551` refactor(store)!: Delete 幂等化——签名改 (int64, error)，0 行返回 (0, nil) **(breaking)**
- `2243cea1` fix: 修 D6（JWT jti）/ D13.2（broker 假成功）/ D12 文档对齐
- `dd1fe34d` fix(appkit): 修复 D16——不可注册的 endpoint 致注册中心启动失败
- `dbcd6a65` refactor(transport): 删死代码、修幽灵契约引用、补 SessionManager 并发安全
- `cab3b42e` refactor(health)!: 健康检查与就绪归位——协议实现不含探针，能力落 health + 装配层 **(breaking)**
- `e1163a71` refactor(bootstrap)!: 服务注册 RegistrarRegistry 自 appkit 迁入 bootstrap——六域变七域；注册中心域文档整合 **(breaking)**
- `30f2bc5f` refactor(registry)!: 服务注册下放顶层独立 module——契约 pkg/registry→bald/registry、后端 contrib/registry/*→registry/<backend> **(breaking)**
- `75ed9558` feat(audit)!: 契约多后端——bconf audit 段 type 单选改 backends 列表（bconf 将升 v0.7.0，GetType 消失 breaking）+ AuditRegistry.Build 按列表序组装 MultiAuditor（重复/空列表/未注册 fail-fast、部分失败逆序回滚、cleanup 聚合逆序回放）+ store/stream 后端 Time 零值兜底修复 + 审计文档统一入口《Bald 审计设计.md》 **(breaking)**
- `abcb995d` feat(audit): 审计步骤4落地——MultiAuditor/LoggerAuditor 上移核心 + contrib/audit-{store,stream} 桥接 + bconf audit 契约段 + FromBootstrap 装配
- `a950ec5b` feat(metrics): Recorder 切 semconv v1.43.0——协议/审计指标正交拆分 + active_requests + RecordActive（破坏性：bald_requests_total / bald_request_duration_seconds 退役）
- `f3470009` fix(log): bslog 文件/轮转句柄经 cleanup 释放——修 Windows 句柄泄漏
- `5b6ba299` chore(berrors): 注释清理——pkg/berrors 残留路径与设计文档引用对齐
- `fef02c80` feat(bootstrap)!: 六域客户端 Registry 自 pkg/appkit 迁入 bootstrap 子模块 **(breaking)**
- `b545307c` refactor(appkit)!: 删除预置插件注册表与 RegisterStoreProvider（零消费，显式装配清账） **(breaking)**
- `1c4a361f` docs: 配置设计文档三篇重整——源层设计独立成篇 + 全景篇代码一致性修正
- `af50551d` refactor(log)!: 契约唯一化瘦身——provider 恒见单后端项，文档代码一致性修正 **(breaking)**
- `9454bec9` refactor(log)!: 收缩日志装配为两级分发，删除零消费入口（v0.5.0 破坏性收缩） **(breaking)**
- `37941566` feat(appkit): 内置路径统一脱敏接线——filter_keys 全后端可达
- `6b509f65` feat(appkit): 内置日志路径四分派——backends 多后端广播接入
- `c52f369c` fix(appkit): 热更新换后端时兑现旧 cleanup——loki 尾批不因热切换丢失
- `41553b16` feat(appkit): 默认日志路径走内置全量注册表——修 logger.type 静默忽略缺陷
- `35b45494` refactor(log): slog 适配器更名 bslog——目录=包名，NewSlogLogger→New
- `906d53ef` fix(appkit): hotReload 落盘改 Reset+Merge——修 vet copylocks
- `e1819d59` refactor(log): 日志调用全量统一包级函数——GetLogger().Xxx 链式写法清零
- `ea8fe483` feat(appkit): FromBootstrap 补业务透传 Option（U1 前置，批 J-1）
- `916a9e1f` refactor(middleware): R1 gin/gRPC 可观测性传输无关核心收敛 shared
- `110abbe5` fix(review): v0.3.0 前置终判——R4 真存疑 5 项全部核实翻案保留
- `501cdb1f` fix(middleware): Observability 注入 trace_id 进 contextx——审计-链路关联根治
- `8a7c81cf` test: R4 复审能力固化批——零调用框架能力补契约测试
- `08c19c5c` fix: 第三轮评审批 B——E11 错误串收敛 + TR4 哑引用 + CR4 argo 超时/状态码 + CR5 反射防御
- `7325c5ea` fix(observability): inbound span 补 SpanKindServer——gin×1 + grpc unary/stream×2
- `3991c013` feat(appkit): 可观测性自动装配——TracerRegistry/MetricsRegistry + otlp contract Provider
- `1eb0cf57` refactor(module): contrib 短名归一为嵌套路径 + 子模块发布准备
- `def57271` chore: 清理 examples——go-bald-admin 已独立为 bald-admin 仓库
- `b537495e` refactor(berrors): 包名 errors→berrors，全仓 import 别名清零
- `b37ce86b` feat(web): 决策⑧落地——统一错误响应体为 google.rpc.Status JSON
- `88c94d0e` feat(pkg): 吸收 bald-utils 的 id/uuid、stringcase、fieldmaskutil
- `2a0925a1` test(middleware): 修 grpc audit 指标测试在 Windows 时钟粒度下的 flaky
- `969f6edc` fix(middleware): no-op tracer 下日志 trace_id/span_id 兜底随机 ID
- `299aab2f` feat(examples): go-bald-admin 移植 T5-T8——文件 MinIO、审计查询、Nacos 注册发现、端到端收尾
- `9ffe811e` feat(appkit,workflow,broker): FromBootstrap 装配扩展——五注册表与 8 段停机链；workflow/argo 与 broker 四后端移植
- `6ce594a8` feat(appkit,database): 数据库客户端契约装配——DatabaseRegistry 多段并存（移植 go-wind-bootstrap/database）
- `5cfed56e` feat(appkit,log,registry): FromBootstrap 约定装配——服务器契约装配/注册中心/日志后端
- `344baa86` feat(store,bconf): 分页契约归一——Where 树化/DataScope 行级/viewer 认证注入
- `e54e72eb` refactor: 顶层模块格局定型——契约/实现分离（berrors/log/transport 独立 module，契约收编 bconf，pkg/conf+config 退役）
- `c40a5e3f` refactor(config): 提升配置契约与源为顶层兄弟模块 bconf/bconfig
- `9bfc7e02` feat: 配置系统抽象
- `bd19d096` refactor(appkit): 拆分超时常量到 types.go + 结构体字段分组与 TODO 注解
- `f5378812` style: gofmt 全量格式化历史未格式化文件（20 文件）
- `148e6d19` fix(pkg): 落地设计评审 D1-D10 修复与 E1-E4/E6 精简
- `76716331` feat(server): 娓呭伩鏈嶅姟绔妧鏈€猴紙鎺㈤拡璺緞/杞闂撮殧鍙厤 + Ready 鑱氬悎锛? 鍓╀綑浜嬮」钀戒綅璁板綍
- `d36f93cc` feat(appkit,codegen): 瀹炰緥 ID 鍞竴鍖?+ P12 鍙嶅悜鐢熸垚楠岃瘉 + codegen 璁捐鏂囨。
- `01825df0` fix(codegen): 淇 P12 gen app --spec 鐢熸垚鐗╄閰?P0 骞惰ˉ杩愯鏃舵祴璇? - 妯℃澘閲嶅啓锛歴ervers/缁勪欢涓€寰嬬粡 appkit.Servers/Components Option 浼犲叆 New   锛堝師鍦?New 鍚?append 姘镐笉鐢熸晥銆丷un 鍓?MountComponent 瑙﹀彂 ErrNotRunning锛?- reconcileAudit 浠庣┖澹冲崌鏍间负鐪熷疄鍙繍琛岋紙log 鍚庣寮€绠卞嵆鐢紝store/stream 鐣?TODO锛?- bundle 鐪熷疄鎺ョ嚎锛圚TTP b.Gin() / gRPC b.GRPCChain()锛夛紱鎺ョ嚎 http_addr/grpc_addr/audit_backends - appkit 鏂板 NewHarness 娴嬭瘯瑁呯疆锛岀Щ闄?SetRunningForTest 娴嬭瘯鍚庨棬 - applyAuditors 鎸夊悗绔悕鎺掑簭娑堥櫎 map 杩唬椤哄簭涓嶇‘瀹?- appspec.proto go_package 淇涓哄疄闄呯敓鎴愯矾寰勫苟閲嶆柊鐢熸垚 - 娴嬭瘯琛ュ己锛氳閰嶉『搴忛槻鍥炲綊鏂█ + 杩愯鏃跺啋鐑燂紙鐢熸垚鐗╃湡瀹炶繍琛?3s 瀛樻椿锛?- 鐢熸垚鐗╃紪璇戠幆澧冩敼涓?_example/bald 娑堣垂鑰呮ā鍧楋紱琛?go.sum锛坢ousetrap 绛?180 鏉★級 - 鏂囨。鍚屾锛氬簲鐢ㄦ鏋惰璁¤惤鍦版竻鍗曘€佹灦鏋勪紭鍖栬矾绾?P12 鐘舵€佷笌瀹℃煡淇銆佹鏋跺绾︽€昏琛?NewHarness/NewReconcileCtx
- `f50d9b02` feat(P12): AppSpec proto 方言驱动 gen app --spec 装配生成
- `d062f8d1` refactor(appkit): R1-2 升级为逐组件 Mount/Unmount 粒度 diff-apply + 测试helper
- `119999f4` feat(appkit): R1-2 期望态 diff-apply 协调 + go-bald-admin 审计后端热切范例
- `8f6d24bd` docs(log): 修正 slog 迁移文档过度声明——轮转能力边界与分级分流语义
- `303553c1` feat(log): align rotate config with proto contract and track generated code
- `d4ff9fa4` feat(core): T1/S1/C1/R1/A1 可组合性原语 + P13 卫生项
- `e387b5f3` feat(core): P10 横切关注点 bundle 门面（双传输链序固化）
- `f95be2bb` feat(core): M8 可观测性指标——与审计同源埋点
- `b8823fe2` feat(core): M7 审计日志——传输中立 + 旁路不阻断
- `5157512a` feat(core): M4 写路径多租户自动注入
- `52e495de` feat(core): P9 授权归一化 AuthzOption 模式上移核心层
- `7dc1a0ff` refactor: 桥接子模块搬入 contrib 并修复 P7/P8/P9 审查问题
- `5e81917b` feat: 实现认证/授权(P7)、多租户(P8)、数据权限(P9)核心抽象与拦截器
- `edd57ac5` feat: 架构演进 P0-P4 落地（分阶段停机/注册表/存储增强/横切对齐/脚手架）并修复分页与过滤语义
- `33c4bdd5` refactor(web): 重构为强绑 gin 的 pkg/web，并新增 grpc-gateway transcoding 示例
- `dcf4aa35` refactor(web): 鐢?gin 閲嶅啓 pkg/web锛屼腑闂翠欢缁熶竴杩佺Щ鑷?pkg/middleware
- `51c395cd` fix(web): JSON 绑定非法即拒绝，并完善示例与文档
- `89e8221b` feat(pkg): 新增 errors/web 包并修复 CR 发现的问题
- `15e6a302` fix(server): 娉ㄥ唽鍦板潃鍙揪鎬т慨澶?閫氶厤绗?host 鏇挎崲涓烘湰鏈?IP)
- `b98cb677` refactor(appkit): 閰嶇疆瀛楁鏀舵暃涓?appConfig 瀵硅薄
- `3ed62bac` refactor(appkit): 鍓ョ鏃ュ織鎸佹湁锛屽洖褰掔函缂栨帓灞傦紙鏂规 A锛? - 绉婚櫎 appkit.Logger Option 涓?AppKit.logger 瀛楁锛孉ppKit 涓嶅啀娉ㄥ叆/鎸佹湁鏃ュ織 - 鍒犻櫎 Run 鍐?log.SetLogger/defer 杩樺師鐨勫叏灞€鍓綔鐢紝鏃ュ織鏀圭粡 log.GetLogger() 娑堣垂 - 鏃ュ織鍚庣閫夋嫨褰掑睘杩涚▼鍏ュ彛 bootstrap 缁熶竴鍒濆鍖栵紝AppKit 鍙秷璐规í鍒囧叏灞€鍙ユ焺 - 鍚屾鏂囨。锛氭棩蹇楄璁?md 绗?鑺傛敼涓鸿繘绋嬪叆鍙ｉ泦鎴愭ā寮忓苟璇存槑鍓ョ缂樼敱锛?  搴旂敤妗嗘灦璁捐.md 鍕炬帀宸插疄鐜扮殑浼橀泤鍋滄満鏃ュ織 TODO
- `16c22858` feat(log): 鏂板 pkg/log 鏃ュ織濂戠害骞舵帴鍏?appkit
- `44fe6ab3` refactor(options): 瀵归綈 onexstack 璁捐锛屾媶鍒嗘枃浠跺苟淇鍦板潃鏍￠獙
- `2ee70b8b` feat(server): symmetric health/readiness probes (HTTP /healthz /readyz + gRPC health); add server-design.md
- `da65c68a` test(appkit/server/registry): P0 unit tests + fix dynamic :0 port registration (BUG-4)
- `f951ef39` feat(config): viper default priority flag>env>local>remote for K8s; fix local watch dropping remote baseline; CR fixes
- `76818b81` chore(bald): 鍗囩骇 kratos 鑷?v3 骞朵慨澶嶉厤缃儹鏇存柊骞跺彂/娈嬬暀鍚堝苟闂

## 2026-11-01

### bald v0.34.1

- `a2552696` docs(architecture): 风险登记册记下 6.5/6.1/4.1/4.2 的消解证据
- `ffa15ffc` test(bconf): 分配剖面守卫按实测重定基线，并写明 -race 档
- `92de7dc6` fix(bundle,codegen): 认证姿态未决定不再静默开放
- `8afe0f3e` fix(appkit): 停机各阶段预算派生自唯一起停入口，反注册失败不再被吞
- `2e9b2d2c` fix(bconf,tls): 契约驱动 TLS 覆盖 gRPC，收敛 gRPC 选项注入路径
- `ad54cdc2` docs(architecture): 入库 5 张渲染快照 SVG
- `70f3f55a` docs(architecture): 装齐工具真实渲染/解析校验，修正图源缺陷
- `344a4c7c` docs(architecture): 新增现状架构、主流程与风险评审制品
- `ca981b14` fix(release): tag 分批推送，避免触发流水线漏跑

### bconf v0.15.3

- `ffa15ffc` test(bconf): 分配剖面守卫按实测重定基线，并写明 -race 档
- `2e9b2d2c` fix(bconf,tls): 契约驱动 TLS 覆盖 gRPC，收敛 gRPC 选项注入路径

### bootstrap v0.13.4

- `2e9b2d2c` fix(bconf,tls): 契约驱动 TLS 覆盖 gRPC，收敛 gRPC 选项注入路径

### transport/gateway v0.4.2

- `2e9b2d2c` fix(bconf,tls): 契约驱动 TLS 覆盖 gRPC，收敛 gRPC 选项注入路径
- `875755c3` chore: 发布 bald v0.34.0 等 13 个模块
- `ec4223fd` chore(deps): 归位 bconf v0.15.1 的消费者 require

### transport/grpc v0.1.3

- `2e9b2d2c` fix(bconf,tls): 契约驱动 TLS 覆盖 gRPC，收敛 gRPC 选项注入路径
- `875755c3` chore: 发布 bald v0.34.0 等 13 个模块
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁

### transport/http v0.1.4

- `2e9b2d2c` fix(bconf,tls): 契约驱动 TLS 覆盖 gRPC，收敛 gRPC 选项注入路径
- `875755c3` chore: 发布 bald v0.34.0 等 13 个模块

### bald v0.34.0

- `89a6a94f` fix(release): 未发版模块也跟随归位内部依赖
- `682739d8` fix(appkit): 测试等启动完成再取消，避免慢机器误报
- `0940278a` test(appkit): 构建约束用例不再依赖宿主平台
- `a3ae07e5` fix(store-gorm): 支持复合主键定位更新
- `50bffe78` docs(store): 补 GORM 桥接的列名与主键解析规则
- `77caa1f5` fix(store)!: Update 注入租户/数据范围隔离谓词 **(breaking)**
- `bba2d0b6` chore(docs): 移除文档头部的 Last updated 元数据
- `fe9fdeac` fix(appkit): 路由冲突检查识别段内通配
- `07904069` style: 清理全仓 gofmt 漂移
- `651e23ed` fix(release): changelog 同日版本合并到同一日期节

### bconf v0.15.2

- `e34875ce` fix(bconf,config): 反序列化不改入参，热更新回调串行化
- `bba2d0b6` chore(docs): 移除文档头部的 Last updated 元数据

### bootstrap v0.13.3

- `ec4223fd` chore(deps): 归位 bconf v0.15.1 的消费者 require
- `e34875ce` fix(bconf,config): 反序列化不改入参，热更新回调串行化
- `07904069` style: 清理全仓 gofmt 漂移

### broker/kafka v0.2.2

- `5ef63d05` fix(kafka): 默认同步发送，避免异步模式静默丢消息
- `07904069` style: 清理全仓 gofmt 漂移

### cache/local v0.1.3

- `a7ddd5f7` fix(cache/local): SetNX 原子化，亚秒 TTL 不再退化为永不过期

### contrib/store-gorm v0.4.0

- `a3ae07e5` fix(store-gorm): 支持复合主键定位更新
- `befb5fb1` fix(store-gorm): 尊重 gorm column tag 与实体真实主键
- `18df9544` test(store-gorm): 补 Update 数据范围隔离回归
- `77caa1f5` fix(store)!: Update 注入租户/数据范围隔离谓词 **(breaking)**
- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁

### contrib/store-mongo v0.2.0

- `77caa1f5` fix(store)!: Update 注入租户/数据范围隔离谓词 **(breaking)**

### registry/consul v0.1.4

- `c293c67d` fix(registry): 注销只停实例心跳；修 etcd 心跳重试阻塞

### registry/etcd v0.3.2

- `c293c67d` fix(registry): 注销只停实例心跳；修 etcd 心跳重试阻塞

### transport/asynq v0.6.0

- `1ad24b56` feat(asynq): 等待任务结果支持 ctx 取消
- `23981a1f` test(asynq): 修正集成测试时序与延迟队列等待
- `efa9d142` test(asynq): 集成测试改为可配置 Redis 并自动跳过
- `bb790612` fix(cron,asynq): 修正停机标记的一次性语义
- `5b43967e` fix(asynq,cron): 停机不再忽略 ctx 截止时间
- `f1a966ff` chore(deps): 对齐 asynq/cron 的 bconf 本地 replace 版本

### transport/cron v0.6.2

- `bb790612` fix(cron,asynq): 修正停机标记的一次性语义
- `5b43967e` fix(asynq,cron): 停机不再忽略 ctx 截止时间
- `f1a966ff` chore(deps): 对齐 asynq/cron 的 bconf 本地 replace 版本

### transport/sse v0.5.0

- `be8ac071` test(sse): 慢订阅者用例逐条消费，消除调度抖动
- `9086bae4` feat(sse): 补慢订阅者与回放截断的可观测性
- `06260f3f` fix(sse): 慢订阅者不再阻塞整流，回放日志改为有界

### transport/webrtc v0.2.1

- `4af0003f` fix(webrtc): 删除未使用的 logDebug
- `9b2aec2a` fix(webrtc): 按 session_id 应用 ICE candidate
- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计

### bald v0.33.1

- `e7b50ef6` fix(release): 变更日志插入不再累积空行
- `7de0202f` fix(release): sync 归位消费者模块的内部依赖
- `3ad6cdd0` fix(appkit): 启动失败在日志后端就绪前兜底写 stderr

### bconf v0.15.1

- `27f43ba9` fix(bconf): 清理契约注释中的日期与批次叙事

### bald v0.33.0

- `bf1f8674` feat: P1 消费侧复用能力落地

### bconf v0.15.0

- `bf1f8674` feat: P1 消费侧复用能力落地

### cache/redis v0.4.0

- `bf1f8674` feat: P1 消费侧复用能力落地

### contrib/config-nacos v0.1.0

- `bf1f8674` feat: P1 消费侧复用能力落地

### contrib/observability-otlp v0.4.0

- `bf1f8674` feat: P1 消费侧复用能力落地

## 2026-10-31

### bald v0.32.0

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `c2e57346` docs: 删除历史路线文档，增加文档路径一致性检查
- `15d09e9f` refactor(appkit,codegen): 按关注点拆分三个大文件
- `b12a8ac9` test(fieldmaskutil): 补 nested_mask 关键路径覆盖
- `b0bef5cd` feat(release): 增加多模块发布计划、依赖归位与变更日志工具
- `4bbf28be` feat(example): 示例域改用默认 REST 绑定并补 e2e
- `43ed27e2` test(ci): 增加 -race 门禁并修复并发缺陷
- `33f19e2e` chore(example): 删除废弃的 bald-gin 示例 module 与外层 go.mod
- `6a64c2f9` refactor(codegen)!: 删除 apiserver 侧挂载镜像生成物，去掉无消费者的 domains.model **(breaking)**
- `cea4e63f` docs: README 30 秒路径补最小 AppSpec 样例，路径可复制即跑
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `ac6a36d5` docs: README 去掉三方设计来源清单
- `599b2127` docs: 架构优化路线 §3.7 去除模板生成器描述
- `e0bf54a8` refactor(codegen)!: 删除 gen app 单文件模板模式，只保留 AppSpec 路径 **(breaking)**
- `fe5f0ec7` chore: 删除仓库根目录的 agent 编排临时文件
- `5fba3495` docs: 面向接手者重构文档，删除考古材料
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### ai/eino v0.1.1

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### ai/langchaingo v0.1.1

- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### ai/openai v0.1.1

- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### bconf v0.14.0

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `6a64c2f9` refactor(codegen)!: 删除 apiserver 侧挂载镜像生成物，去掉无消费者的 domains.model **(breaking)**
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### bconfig v0.2.1

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### bootstrap v0.13.2

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `c2e57346` docs: 删除历史路线文档，增加文档路径一致性检查
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
- `90eb0dde` chore: W1 卫生波——非导出死代码清零 + internal/ 免费改名 + 依赖面 tidy 修复 + CI tidy 闸门
- `843d3d2a` chore: 清理注释中的批次/排期/迁移痕迹——只留代码逻辑注释

### broker/kafka v0.2.1

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### broker/rabbitmq v0.2.1

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### broker/redis v0.4.1

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### broker/rocketmq v0.2.1

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### cache/local v0.1.2

- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### cache/redis v0.3.1

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### circuitbreaker/sres v0.1.3

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### cobramcp v0.3.4

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### contrib/audit-gorm v0.2.2

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
- `90eb0dde` chore: W1 卫生波——非导出死代码清零 + internal/ 免费改名 + 依赖面 tidy 修复 + CI tidy 闸门

### contrib/audit-stream v0.4.1

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
- `90eb0dde` chore: W1 卫生波——非导出死代码清零 + internal/ 免费改名 + 依赖面 tidy 修复 + CI tidy 闸门

### contrib/database/gorm v0.1.1

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
- `90eb0dde` chore: W1 卫生波——非导出死代码清零 + internal/ 免费改名 + 依赖面 tidy 修复 + CI tidy 闸门

### contrib/database/mongodb v0.1.1

- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
- `90eb0dde` chore: W1 卫生波——非导出死代码清零 + internal/ 免费改名 + 依赖面 tidy 修复 + CI tidy 闸门

### contrib/observability-otlp v0.3.2

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `c2e57346` docs: 删除历史路线文档，增加文档路径一致性检查
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
- `90eb0dde` chore: W1 卫生波——非导出死代码清零 + internal/ 免费改名 + 依赖面 tidy 修复 + CI tidy 闸门
- `843d3d2a` chore: 清理注释中的批次/排期/迁移痕迹——只留代码逻辑注释

### contrib/store-mongo v0.1.2

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
- `90eb0dde` chore: W1 卫生波——非导出死代码清零 + internal/ 免费改名 + 依赖面 tidy 修复 + CI tidy 闸门

### log v0.5.4

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
- `843d3d2a` chore: 清理注释中的批次/排期/迁移痕迹——只留代码逻辑注释

### log/aliyun v0.2.2

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### log/charm v0.2.2

- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### log/loki v0.2.2

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### log/sentry v0.2.2

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### log/tencent v0.2.2

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### metrics v0.1.2

- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### oss/minio v0.3.1

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### oss/s3 v0.3.1

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### ratelimit/bbr v0.1.1

- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### registry v0.3.1

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### registry/consul v0.1.3

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
- `90eb0dde` chore: W1 卫生波——非导出死代码清零 + internal/ 免费改名 + 依赖面 tidy 修复 + CI tidy 闸门

### registry/etcd v0.3.1

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### registry/kubernetes v0.1.4

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
- `90eb0dde` chore: W1 卫生波——非导出死代码清零 + internal/ 免费改名 + 依赖面 tidy 修复 + CI tidy 闸门

### registry/nacos v0.3.1

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### transport/asynq v0.5.1

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### transport/cron v0.6.1

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### transport/gateway v0.4.1

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### transport/http v0.1.3

- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### transport/redis v0.2.1

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### transport/sse v0.4.1

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `43ed27e2` test(ci): 增加 -race 门禁并修复并发缺陷
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### transport/tcp v0.1.2

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
- `90eb0dde` chore: W1 卫生波——非导出死代码清零 + internal/ 免费改名 + 依赖面 tidy 修复 + CI tidy 闸门

### transport/websocket v0.1.1

- `43ed27e2` test(ci): 增加 -race 门禁并修复并发缺陷
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### workflow/argo v0.2.1

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
