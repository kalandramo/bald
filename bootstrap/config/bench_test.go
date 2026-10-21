package config

import "testing"

// bench_test.go 为配置仓库（Store）的基准套件。
//
// 口径纪律：
//   - 层缓存 map（layerM 各层的 m）在计时外构造一次；remerge 是 copy-on-write
//     深合并，每 op 产出全新合并树，与热更新路径（setupWatch 的 apply）同构；
//   - remerge 约定「调用方须已持有写锁」——基准单 goroutine 独占 Store，
//     不持锁（锁本身不重入，直接调用即是其约定形态）；
//   - 热路径无 fmt/日志。

// benchLayerMaps 构造三层缓存：远程基准层（低）→ 契约层 → 本地覆盖层（高），
// 树形状对齐契约 15 段的子集并带嵌套覆盖点（同名键多层出现，深合并必经路径）。
func benchLayerMaps() (remote, contract, local map[string]any) {
	remote = map[string]any{
		"app": map[string]any{"name": "bench", "version": "v0.24.0", "env": "remote"},
		"server": map[string]any{
			"http": map[string]any{"addr": ":8080", "driver": "nethttp"},
			"grpc": map[string]any{"addr": ":9090", "reflection": true},
		},
		"logger": map[string]any{
			"backends": []any{map[string]any{"type": "slog", "slog": map[string]any{"level": "info", "format": "console", "outputPath": "stdout"}}},
		},
		"database": map[string]any{
			"sql": map[string]any{"driver": "mysql", "maxIdleConnections": 2, "maxOpenConnections": 16},
		},
		"cache":  map[string]any{"redis": map[string]any{"addr": "127.0.0.1:6379", "db": 1}},
		"broker": map[string]any{"kafka": map[string]any{"brokers": []any{"127.0.0.1:9092"}, "groupId": "bench"}},
		"metrics": map[string]any{
			"type":       "prometheus",
			"prometheus": map[string]any{"addr": ":9100", "path": "/metrics"},
		},
		"audit": map[string]any{"backends": []any{"log"}},
	}
	contract = map[string]any{
		"app": map[string]any{"version": "v0.25.0", "env": "bench", "stopTimeout": "30s"},
		"server": map[string]any{
			"http": map[string]any{"addr": ":18080", "middleware": map[string]any{
				"timeout": map[string]any{"defaultTimeoutMs": 5000},
			}},
		},
		"logger": map[string]any{
			"backends":   []any{map[string]any{"type": "slog", "slog": map[string]any{"level": "debug", "format": "json", "outputPath": "stdout"}}},
			"filterKeys": []any{"password"},
		},
		"database": map[string]any{
			"sql":     map[string]any{"source": "bench:bench@tcp(127.0.0.1:3306)/bench", "enableTrace": true, "maxOpenConnections": 32},
			"mongodb": map[string]any{"uri": "mongodb://127.0.0.1:27017/bench", "database": "bench"},
		},
		"cache": map[string]any{"local": map[string]any{"size": 1024, "defaultTtlSeconds": 60}},
	}
	local = map[string]any{
		"server":         map[string]any{"http": map[string]any{"addr": ":28080"}},
		"logger":         map[string]any{"filterKeys": []any{"password", "token"}},
		"database":       map[string]any{"sql": map[string]any{"maxOpenConnections": 64}},
		"opsMaintenance": true, // 表外运维键：重合并原样透传
	}
	return remote, contract, local
}

// benchStore 构造带 env/flag 层的完整 Store（不经 Load——避免文件发现与
// watch 装配；层缓存与 Load 产出的 layerState 同构）。
func benchStore() *Store {
	remote, contract, local := benchLayerMaps()
	return &Store{
		layerM: []layerState{
			{spec: Layer{Name: "remote"}, m: remote},
			{spec: Layer{Name: "contract"}, m: contract},
			{spec: Layer{Name: "local"}, m: local},
		},
		envM:  environMap("BENCH"),
		flagM: flattenFlags(nil),
	}
}

// BenchmarkStoreRemerge_Layers 度量 remerge（config.go:311）的全量重合并成本：
// 三层缓存 + env + flag 逐层 copy-on-write 深合并。这是热更新任一层变更后
// 每次触发的路径成本（含 OnChange 前的树重建），也是快照语义的地基。
func BenchmarkStoreRemerge_Layers(b *testing.B) {
	s := benchStore()
	s.merged = s.remerge() // 预热合并树，使各 op 起点一致
	b.ReportAllocs()
	for b.Loop() {
		m := s.remerge()
		if m == nil {
			b.Fatal("remerge returned nil")
		}
	}
}

// BenchmarkStoreMountEnv 度量 MountEnv（config.go:325，B6 两遍合并第二遍）
// 的完整成本：env 前缀派生（os.Environ 全表扫描 + 前缀过滤）+ env 层重建 +
// 全量重合并（含写锁）。仅基线项：MountEnv 为本版新增 API，v0.24.0 无对应路径。
//
// 注意：os.Environ 扫描成本随进程环境表大小浮动——绝对值只在同机同环境下
// 复跑可比；对照表的 remerge 项不含此成本。
func BenchmarkStoreMountEnv(b *testing.B) {
	s := benchStore()
	s.envM = nil // DeferEnv 形态：env 层缺位，由 MountEnv 挂载
	for b.Loop() {
		s.MountEnv("BENCH")
	}
}
