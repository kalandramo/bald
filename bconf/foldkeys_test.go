package bconf

import (
	"reflect"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// foldTree 按点路径造出 environMap 的落树形状：env 名
// BALD_X_SERVER_HTTP_MIDDLEWARE_RATE_LIMIT_BURST 切成
// server.http.middleware.rate.limit.burst。
func foldTree(leaves map[string]any) map[string]any {
	m := map[string]any{}
	for path, v := range leaves {
		parts := strings.Split(path, ".")
		cur := m
		for i, p := range parts {
			if i == len(parts)-1 {
				cur[p] = v
				break
			}
			sub, ok := cur[p].(map[string]any)
			if !ok {
				sub = map[string]any{}
				cur[p] = sub
			}
			cur = sub
		}
	}
	return m
}

// TestFoldEnvKeys_MultiWordChain 钉住本函数存在的首要理由：多词契约字段
// （rate_limit / default_timeout_ms / max_open_connections）在 env 里的
// 合法注入姿势。
//
// 断言分两层——树形状（折叠的直接产物）与 Unmarshal 后的契约值（端到端：
// 折错了名字，值会被 DiscardUnknown 静默丢掉）。
func TestFoldEnvKeys_MultiWordChain(t *testing.T) {
	m := foldTree(map[string]any{
		"server.http.middleware.rate.limit.burst":           "3",
		"server.http.middleware.timeout.default.timeout.ms": "50",
		"database.sql.max.open.connections":                 "64",
		"database.sql.enable.trace":                         "true",
	})
	FoldEnvKeys(m, NewBootstrap())

	want := map[string]any{
		"server": map[string]any{"http": map[string]any{"middleware": map[string]any{
			"rate_limit": map[string]any{"burst": "3"},
			"timeout":    map[string]any{"default_timeout_ms": "50"},
		}}},
		"database": map[string]any{"sql": map[string]any{
			"max_open_connections": "64",
			"enable_trace":         "true",
		}},
	}
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("folded tree =\n%v\nwant\n%v", m, want)
	}

	var cfg bootstrapv1.BootstrapConfig
	if err := UnmarshalMap(m, &cfg); err != nil {
		t.Fatalf("UnmarshalMap: %v", err)
	}
	if got := cfg.GetServer().GetHttp().GetMiddleware().GetRateLimit().GetBurst(); got != 3 {
		t.Errorf("server.http.middleware.rate_limit.burst = %d, want 3", got)
	}
	if got := cfg.GetServer().GetHttp().GetMiddleware().GetTimeout().GetDefaultTimeoutMs(); got != 50 {
		t.Errorf("server.http.middleware.timeout.default_timeout_ms = %d, want 50", got)
	}
	if got := cfg.GetDatabase().GetSql().GetMaxOpenConnections(); got != 64 {
		t.Errorf("database.sql.max_open_connections = %d, want 64", got)
	}
	if !cfg.GetDatabase().GetSql().GetEnableTrace() {
		t.Error("database.sql.enable_trace = false, want true")
	}

	// 折叠后严格门禁必须无话可说：这条注入姿势不是笔误。
	if unknown := FindUnknownKeys(m, NewBootstrap()); len(unknown) != 0 {
		t.Errorf("unknown keys after fold = %q, want none", unknown)
	}
}

// TestFoldEnvKeys_ChainThenRecurse 钉住跨层组合：折叠出来的字段值本身还是
// 多词键（request_id.header_name、allowed_origins 的祖先链）——下钻必须在
// 折叠之后继续，否则只折了第一层。
func TestFoldEnvKeys_ChainThenRecurse(t *testing.T) {
	m := foldTree(map[string]any{
		"server.http.middleware.request.id.header.name": "X-Trace-Id",
		"server.http.middleware.cors.allowed.origins":   "https://a.example",
		"server.http.middleware.logging.skip.paths":     "/healthz",
		"app.stop.timeout": "45s",
	})
	FoldEnvKeys(m, NewBootstrap())

	var cfg bootstrapv1.BootstrapConfig
	if err := UnmarshalMap(m, &cfg); err != nil {
		t.Fatalf("UnmarshalMap: %v", err)
	}
	mw := cfg.GetServer().GetHttp().GetMiddleware()
	if got := mw.GetRequestId().GetHeaderName(); got != "X-Trace-Id" {
		t.Errorf("request_id.header_name = %q, want X-Trace-Id", got)
	}
	if got := mw.GetCors().GetAllowedOrigins(); len(got) != 1 || got[0] != "https://a.example" {
		t.Errorf("cors.allowed_origins = %v, want [https://a.example]", got)
	}
	if got := mw.GetLogging().GetSkipPaths(); len(got) != 1 || got[0] != "/healthz" {
		t.Errorf("logging.skip_paths = %v, want [/healthz]", got)
	}
	if got := cfg.GetApp().GetStopTimeout().AsDuration().Seconds(); got != 45 {
		t.Errorf("app.stop_timeout = %v, want 45s", got)
	}
	if unknown := FindUnknownKeys(m, NewBootstrap()); len(unknown) != 0 {
		t.Errorf("unknown keys after fold = %q, want none", unknown)
	}
}

// TestFoldEnvKeys_SiblingLeavesKept 证明「唯一链」判据不误伤同字段多键：
// rate 与 burst 两个 env 各自切出的链在 rate_limit 处汇合，汇合点之后的
// 兄弟键属于字段值，不参与唯一性判定。
func TestFoldEnvKeys_SiblingLeavesKept(t *testing.T) {
	m := foldTree(map[string]any{
		"server.grpc.middleware.rate.limit.rate":  "10",
		"server.grpc.middleware.rate.limit.burst": "20",
	})
	FoldEnvKeys(m, NewBootstrap())

	var cfg bootstrapv1.BootstrapConfig
	if err := UnmarshalMap(m, &cfg); err != nil {
		t.Fatalf("UnmarshalMap: %v", err)
	}
	rl := cfg.GetServer().GetGrpc().GetMiddleware().GetRateLimit()
	if rl.GetRate() != 10 || rl.GetBurst() != 20 {
		t.Errorf("server.grpc.middleware.rate_limit = rate %v burst %v, want 10/20", rl.GetRate(), rl.GetBurst())
	}
}

// TestFoldEnvKeys_NoFalsePositive 反证：折叠只在唯一链上发生，不得凭空造字段，
// 也不得覆盖用户已经写对的键。
//
// 三种形状必须原样留给严格门禁：分支链（切分点不唯一——猜出来的键名比报出来
// 的未知键更危险）、规范键已被另一种形状占据、业务自持段（框架没有它的形状知识）。
func TestFoldEnvKeys_NoFalsePositive(t *testing.T) {
	branching := foldTree(map[string]any{
		"server.http.middleware.rate.limit.burst": "1",
		"server.http.middleware.rate.other":       "2",
	})
	before := deepCloneMap(branching)
	FoldEnvKeys(branching, NewBootstrap())
	if !reflect.DeepEqual(branching, before) {
		t.Fatalf("分支链被改写：\n%v\nwant 原样\n%v", branching, before)
	}
	if unknown := FindUnknownKeys(branching, NewBootstrap()); len(unknown) == 0 {
		t.Error("分支链既没折叠也没被门禁判成未知键（静默丢弃）")
	}

	// 规范键已存在：折叠会覆盖用户的显式写法，必须让位。
	occupied := foldTree(map[string]any{
		"database.sql.max_open_connections": "32",
		"database.sql.max.open.connections": "64",
	})
	FoldEnvKeys(occupied, NewBootstrap())
	sql := occupied["database"].(map[string]any)["sql"].(map[string]any)
	if got := sql["max_open_connections"]; got != "32" {
		t.Errorf("已有的规范键被嵌套链覆盖成 %v，want 32（显式写法必须赢）", got)
	}
	if _, ok := sql["max"]; !ok {
		t.Error("未折叠的嵌套链被吞掉（应原样保留，由门禁报未知键）")
	}

	// 规范键被 JSON 名占据（maxOpenConnections）：两种写法在读侧命中同一字段，
	// 折叠会把其中一份静默丢掉，同样必须让位。
	byJSON := foldTree(map[string]any{
		"database.sql.maxOpenConnections":   "32",
		"database.sql.max.open.connections": "64",
	})
	FoldEnvKeys(byJSON, NewBootstrap())
	sql2 := byJSON["database"].(map[string]any)["sql"].(map[string]any)
	if got := sql2["maxOpenConnections"]; got != "32" {
		t.Errorf("JSON 名写法被嵌套链覆盖成 %v，want 32", got)
	}
	if _, ok := sql2["max"]; !ok {
		t.Error("冲突时未折叠的链被吞掉（应原样保留，由门禁报未知键）")
	}

	// 业务段：框架不猜它的形状。
	business := foldTree(map[string]any{"login.rate.limit.rate": "1"})
	bizBefore := deepCloneMap(business)
	FoldEnvKeys(business, NewBootstrap())
	if !reflect.DeepEqual(business, bizBefore) {
		t.Errorf("业务段被改写：\n%v\nwant 原样\n%v", business, bizBefore)
	}
	if unknown := FindUnknownKeys(business, NewBootstrap()); len(unknown) == 0 {
		t.Error("业务段未知键没被门禁看见")
	}
}

// TestFoldEnvKeys_GuardAndNoop 钉边界：nil 入参不 panic，已规范化的树整棵不动
// （幂等——重复调用不得二次变形）。
func TestFoldEnvKeys_GuardAndNoop(t *testing.T) {
	FoldEnvKeys(nil, NewBootstrap())
	FoldEnvKeys(map[string]any{"app": map[string]any{}}, nil)

	canon := map[string]any{
		"app":    map[string]any{"stop_timeout": "30s"},
		"server": map[string]any{"http": map[string]any{"addr": ":8080"}},
	}
	once := deepCloneMap(canon)
	FoldEnvKeys(canon, NewBootstrap())
	if !reflect.DeepEqual(canon, once) {
		t.Errorf("规范树被改写：%v", canon)
	}
	FoldEnvKeys(canon, NewBootstrap())
	if !reflect.DeepEqual(canon, once) {
		t.Errorf("二次调用改写：%v", canon)
	}
}

// TestFoldEnvKeys_ListAndMapFields 钉住下钻规则：repeated message 的每个元素
// 都折（logger.backends），map 字段的键由用户自持、整棵跳过。
//
// map 那段不是摆设：labels 里恰好长成兄弟字段形状的键（batch.size）若被下钻，
// 会被折成 loki.batch_size——用户的流标签凭空变成一个配置项、并丢掉原键。
func TestFoldEnvKeys_ListAndMapFields(t *testing.T) {
	m := map[string]any{
		"logger": map[string]any{
			"backends": []any{
				map[string]any{"type": "slog", "slog": map[string]any{"output": map[string]any{"path": "stdout"}}},
				map[string]any{"type": "loki", "loki": map[string]any{
					"labels": map[string]any{"batch": map[string]any{"size": "9"}},
				}},
			},
		},
	}
	FoldEnvKeys(m, NewBootstrap())

	backends := m["logger"].(map[string]any)["backends"].([]any)
	slog := backends[0].(map[string]any)["slog"].(map[string]any)
	if got := slog["output_path"]; got != "stdout" {
		t.Errorf("repeated 元素内的多词键没折：slog = %v", slog)
	}
	loki := backends[1].(map[string]any)["loki"].(map[string]any)
	if _, ok := loki["batch_size"]; ok {
		t.Errorf("map 字段的键被折成了兄弟字段：%v", loki)
	}
	labels := loki["labels"].(map[string]any)
	if got := labels["batch"].(map[string]any)["size"]; got != "9" {
		t.Errorf("map 字段的键被动了：labels = %v", labels)
	}
}
