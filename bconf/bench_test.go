package bconf

import "testing"

// bench_test.go 为 bconf 契约核心的基准套件。对照方法与口径见
// docs/devel/zh-CN/性能基准.md。
//
// 口径纪律：
//   - fixture 构造全部在计时外（b.Loop 之前）；
//   - 热路径无 fmt/日志；
//   - 契约树以 JSON name 规范形书写。UnmarshalMap 会把输入树写回规范形
//     （coerceMessage/foldPath），首轮迭代后为稳态，后续轮次口径一致。

// benchFullContractTree 构造全 15 段契约树（bootstrap.proto 顶层段：app/server/
// config/registry/logger/tracer/metrics/broker/storage/ai/workflow/cache/script/
// database/audit），每段取 1-2 个代表性子段，覆盖标量/duration/enum/repeated/
// 嵌套消息 coerce 分支。键统一 JSON name（protojson 规范形）。
//
// 刻意不含 map<string,string> 字段（otlp.headers / loki.labels）：map 路径
// 由 TestUnmarshalMap_MapFieldReplace 单独覆盖，此处保持 fixture 聚焦在
// 标量/duration/enum/repeated/嵌套消息的 coerce 分支上。
func benchFullContractTree() map[string]any {
	return map[string]any{
		"app": map[string]any{
			"id":          "bench-app",
			"name":        "bench",
			"version":     "v0.25.0",
			"env":         "bench",
			"stopTimeout": "30s",
		},
		"server": map[string]any{
			"http": map[string]any{
				"addr":   ":8080",
				"driver": "nethttp",
				"middleware": map[string]any{
					"cors": map[string]any{
						"allowedOrigins":   []any{"https://bench.local"},
						"allowedMethods":   []any{"GET", "POST"},
						"allowCredentials": true,
						"maxAge":           3600,
					},
					"requestId": map[string]any{"headerName": "X-Request-Id"},
					"timeout":   map[string]any{"defaultTimeoutMs": 5000, "statusCode": 504},
				},
			},
			"grpc": map[string]any{
				"addr":       ":9090",
				"reflection": true,
				"middleware": map[string]any{
					"recovery": map[string]any{},
					"logging":  map[string]any{},
				},
			},
			"kafka": map[string]any{"addrs": []any{"127.0.0.1:9092"}, "codec": "json"},
		},
		"config": map[string]any{
			"file":  map[string]any{"path": "config.yaml", "format": "yaml", "watch": true},
			"nacos": map[string]any{"serverAddrs": []any{"127.0.0.1:8848"}, "namespace": "bench", "dataId": "bench.yaml", "format": "yaml"},
		},
		"registry": map[string]any{
			"type":   "consul",
			"consul": map[string]any{"address": "127.0.0.1:8500", "scheme": "http", "healthCheckInterval": 5},
		},
		"logger": map[string]any{
			"backends": []any{
				map[string]any{
					"type": "slog",
					"slog": map[string]any{"level": "info", "format": "console", "outputPath": "stdout"},
				},
				map[string]any{
					"type": "loki",
					"loki": map[string]any{
						"endpoint":      "http://127.0.0.1:3100",
						"batchSize":     100,
						"flushInterval": 1,
					},
				},
			},
			"filterKeys": []any{"password", "token"},
		},
		"tracer": map[string]any{
			"type": "otlp",
			"otlp": map[string]any{
				"endpoint":    "127.0.0.1:4317",
				"insecure":    true,
				"sampler":     "parentbased_traceidratio",
				"sampleRatio": 0.1,
			},
		},
		"metrics": map[string]any{
			"type":       "prometheus",
			"prometheus": map[string]any{"addr": ":9100", "path": "/metrics"},
		},
		"broker": map[string]any{
			"kafka": map[string]any{
				"brokers":  []any{"127.0.0.1:9092"},
				"groupId":  "bench-group",
				"authType": "none",
			},
		},
		"storage": map[string]any{
			"minio": map[string]any{
				"endpoint":  "127.0.0.1:9000",
				"accessKey": "bench",
				"secretKey": "bench-secret",
				"useSsl":    false,
			},
		},
		"ai": map[string]any{
			"openai": map[string]any{
				"modelType":      1,
				"modelName":      "gpt-bench",
				"timeoutSeconds": 30,
				"cloud":          map[string]any{"apiKey": "sk-bench", "baseUrl": "http://127.0.0.1:8000/v1"},
			},
		},
		"workflow": map[string]any{
			"temporal": map[string]any{"hostPort": "127.0.0.1:7233", "namespace": "bench"},
		},
		"cache": map[string]any{
			"local": map[string]any{"size": 1024, "defaultTtlSeconds": 60},
			"redis": map[string]any{"addr": "127.0.0.1:6379", "db": 1, "keyPrefix": "bench:"},
		},
		"script": map[string]any{
			"engine": "LUA",
			"options": map[string]any{
				"enabled":        true,
				"paths":          []any{"scripts"},
				"entry":          "main.lua",
				"preLoadScripts": []any{"lib.lua"},
				"hotReload":      false,
			},
			"source": map[string]any{"type": "FILE", "paths": []any{"scripts"}, "cacheTtl": "5m"},
			"pool":   map[string]any{"initial": 2, "max": 8},
		},
		"database": map[string]any{
			"sql": map[string]any{
				"driver":                       "mysql",
				"source":                       "bench:bench@tcp(127.0.0.1:3306)/bench",
				"migrate":                      false,
				"enableTrace":                  true,
				"enableMetrics":                true,
				"maxIdleConnections":           2,
				"maxOpenConnections":           16,
				"connectionMaxLifetimeSeconds": 3600,
			},
			"mongodb": map[string]any{"uri": "mongodb://127.0.0.1:27017/bench", "database": "bench", "timeoutSeconds": 5},
		},
		"audit": map[string]any{
			"backends": []any{"log"},
			"store":    map[string]any{"migrate": false},
			"stream":   map[string]any{"stream": "audit", "buffer": 128},
		},
	}
}

// BenchmarkUnmarshalMap_FullContract 度量 UnmarshalMap 的全 15 段契约反序列化
// 完整成本：coerce 类型规范化 → json.Marshal → protojson.Unmarshal →
// clearPresentLists → proto.Merge 进带默认值的契约，即配置装载的出口成本。
//
// 每 op 经 NewBootstrap() 取带默认值的空契约（含 os.Hostname 取默认 App.Id——
// 与真实装配路径一致）；契约树构造在计时外，重用同一棵（首轮写回规范形后为
// 稳态）。
func BenchmarkUnmarshalMap_FullContract(b *testing.B) {
	tree := benchFullContractTree()
	// 计时外自检：15 段必须全部落位。字段名写错会被 protojson 的
	// DiscardUnknown 静默丢弃——这里 fail-fast，防止基准悄悄缩水导致
	// 数字失真。
	{
		probe := NewBootstrap()
		if err := UnmarshalMap(benchFullContractTree(), probe); err != nil {
			b.Fatal(err)
		}
		for i, seg := range []any{
			probe.GetApp(), probe.GetServer(), probe.GetConfig(), probe.GetRegistry(),
			probe.GetLogger(), probe.GetTracer(), probe.GetMetrics(), probe.GetBroker(),
			probe.GetStorage(), probe.GetAi(), probe.GetWorkflow(), probe.GetCache(),
			probe.GetScript(), probe.GetDatabase(), probe.GetAudit(),
		} {
			if seg == nil {
				b.Fatalf("section %d did not populate (typo in fixture key?)", i)
			}
		}
		if probe.GetApp().GetId() != "bench-app" || probe.GetDatabase().GetSql().GetDriver() != "mysql" {
			b.Fatal("spot values missing: fixture keys do not match proto field names")
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		cfg := NewBootstrap()
		if err := UnmarshalMap(tree, cfg); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFindUnknownKeys_StrictScan 度量 FindUnknownKeys 的全树扫描成本——
// StrictContract 严格契约校验（config.Load 的 opt-in 路径与 gen 产物 Gate3）的
// 底层成本：逐层比对 map 键与字段描述符并按路径排序输出。
//
// 在契约树上叠加 3 个不同深度的未知键（顶层 1 + 嵌套 2），使命中分支与
// 「未知键收集」分支都被覆盖；树只读，计时外构造一次复用。
func BenchmarkFindUnknownKeys_StrictScan(b *testing.B) {
	tree := benchFullContractTree()
	tree["telemetry"] = map[string]any{"enabled": true}
	tree["server"].(map[string]any)["http"].(map[string]any)["unknownFlag"] = "x"
	tree["database"].(map[string]any)["sql"].(map[string]any)["legacyParam"] = 1
	cfg := NewBootstrap()
	b.ReportAllocs()
	for b.Loop() {
		if unknown := FindUnknownKeys(tree, cfg); len(unknown) != 3 {
			b.Fatalf("want 3 unknown keys, got %v", unknown)
		}
	}
}

// TestUnmarshalMap_MapFieldReplace 钉住 map 字段替换语义的回归测试：
// map 字段的 Kind 是合成 entry 消息，若被落入 MessageKind 分支，
// Mutable(fd).Message() 对 map 直接 panic——契约里任何非空
// map<string,string>（tracer.otlp.headers、logger.loki.labels）都会崩溃。
// map 与 repeated 同为「替换」语义：二次 Unmarshal 的 map 整体覆盖，不残留
// 首次条目。
func TestUnmarshalMap_MapFieldReplace(t *testing.T) {
	cfg := NewBootstrap()
	first := map[string]any{
		"tracer": map[string]any{
			"type": "otlp",
			"otlp": map[string]any{"endpoint": "127.0.0.1:4317", "headers": map[string]any{"env": "bench", "legacy": "1"}},
		},
	}
	if err := UnmarshalMap(first, cfg); err != nil {
		t.Fatalf("first unmarshal: %v", err)
	}
	if got := cfg.GetTracer().GetOtlp().GetHeaders(); got["env"] != "bench" {
		t.Fatalf("headers after first unmarshal: %v", got)
	}
	second := map[string]any{
		"tracer": map[string]any{
			"type": "otlp",
			"otlp": map[string]any{"endpoint": "127.0.0.1:4318", "headers": map[string]any{"env": "prod"}},
		},
	}
	if err := UnmarshalMap(second, cfg); err != nil {
		t.Fatalf("second unmarshal: %v", err)
	}
	got := cfg.GetTracer().GetOtlp().GetHeaders()
	if got["env"] != "prod" || len(got) != 1 {
		t.Fatalf("map must replace wholesale, got %v", got)
	}
	if e := cfg.GetTracer().GetOtlp().GetEndpoint(); e != "127.0.0.1:4318" {
		t.Fatalf("scalar overwrite broken: %q", e)
	}
}

// BenchmarkValidate_FullContract 度量 bconf.Validate 对全契约
// 的启动前校验成本：app 段非空、server 双地址格式、logger 六种后端段级规则
// （slog+loki 两项，覆盖 repeated 逐项校验）。
//
// 被校验契约在计时外由 NewBootstrap + UnmarshalMap 一次定型；Validate 只读。
func BenchmarkValidate_FullContract(b *testing.B) {
	cfg := NewBootstrap()
	if err := UnmarshalMap(benchFullContractTree(), cfg); err != nil {
		b.Fatal(err)
	}
	if err := Validate(cfg); err != nil {
		b.Fatalf("fixture must validate: %v", err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := Validate(cfg); err != nil {
			b.Fatal(err)
		}
	}
}
