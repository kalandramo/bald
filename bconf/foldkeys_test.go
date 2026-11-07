package bconf

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"

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

// TestFoldEnvKeys_SiblingLeavesKept 钉住汇合点之后的兄弟键：rate 与 burst 两个
// env 各自切出的链在 rate_limit 处汇合，汇合点之后它们是字段值自己的键，
// 不参与拼接（拼的是 rate_limit，不是 rate_limit_rate）。
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

// TestFoldEnvKeys_NoFalsePositive 反证：折叠不得凭空造字段，也不得覆盖用户已经
// 写对的键。
//
// 三种形状必须留给严格门禁：拼不出契约名的分支（猜出来的键名比报出来的未知键更
// 危险）、规范键已被另一种形状占据、业务自持段（框架没有它的形状知识）。
func TestFoldEnvKeys_NoFalsePositive(t *testing.T) {
	// 同一词头下一条合法一条笔误：合法的照折，笔误的原样留下并被门禁看见。
	// 两条互不牵连——「有兄弟就不折」会让合法的那条也静默失效。
	branching := foldTree(map[string]any{
		"server.http.middleware.rate.limit.burst": "1",
		"server.http.middleware.rate.other":       "2",
	})
	FoldEnvKeys(branching, NewBootstrap())
	mw := branching["server"].(map[string]any)["http"].(map[string]any)["middleware"].(map[string]any)
	rl, ok := mw["rate_limit"].(map[string]any)
	if !ok || rl["burst"] != "1" {
		t.Errorf("合法分支没折：middleware = %v", mw)
	}
	rate, ok := mw["rate"].(map[string]any)
	if !ok || rate["other"] != "2" || len(rate) != 1 {
		t.Errorf("笔误分支被改写或被吞掉：rate = %v，want 原样 map[other:2]", rate)
	}
	if _, ok := mw["rate"].(map[string]any)["limit"]; ok {
		t.Error("被消费的叶子路径没删干净（rate.limit 应已清理）")
	}
	if unknown := FindUnknownKeys(branching, NewBootstrap()); len(unknown) != 1 ||
		unknown[0] != ".server.http.middleware.rate" {
		t.Errorf("门禁没看见笔误分支：unknown = %q，want [.server.http.middleware.rate]", unknown)
	}

	// 同一判据在反方向也必须成立：笔误分支字典序在前，不能被它连累到合法分支。
	// 只测正序等于只钉住了「实现顺序恰好友好」。
	reversed := foldTree(map[string]any{
		"server.http.middleware.rate.aaa":         "2",
		"server.http.middleware.rate.limit.burst": "1",
	})
	FoldEnvKeys(reversed, NewBootstrap())
	mw2 := reversed["server"].(map[string]any)["http"].(map[string]any)["middleware"].(map[string]any)
	if rl2, ok := mw2["rate_limit"].(map[string]any); !ok || rl2["burst"] != "1" {
		t.Errorf("笔误分支字典序在前时合法分支没折：middleware = %v", mw2)
	}
	if _, ok := mw2["rate"].(map[string]any)["aaa"]; !ok {
		t.Errorf("笔误分支被吞掉：middleware = %v", mw2)
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

	// JSON 名写法同样是本层字段名（lookupFields 双注册），必须走「平级规范名优先」
	// 而不是被当成待折的嵌套链——这条不变量是那条规则今天唯一可观测的作用面。
	alias := map[string]any{"app": map[string]any{"stopTimeout": "30s"}}
	aliasOnce := deepCloneMap(alias)
	FoldEnvKeys(alias, NewBootstrap())
	if !reflect.DeepEqual(alias, aliasOnce) {
		t.Errorf("JSON 名写法被改写：%v，want 原样 %v", alias, aliasOnce)
	}
}

// TestFoldEnvKeys_SiblingPrefixUppercaseEnv 用 environMap 的真实落树形状复现
// 修复前必然失效的那条注入姿势：BALD_APP_PROBE_LIVE_PATH 与
// BALD_APP_PROBE_READY_PATH 一起注入。键是大写的（env 原名），且两条链在 probe
// 处分支——旧判据「子节点唯一才折」在这里两条都折不了，值被 DiscardUnknown 丢掉。
func TestFoldEnvKeys_SiblingPrefixUppercaseEnv(t *testing.T) {
	m := map[string]any{"APP": map[string]any{"PROBE": map[string]any{
		"LIVE":  map[string]any{"PATH": "/livez"},
		"READY": map[string]any{"PATH": "/readyz"},
	}}}
	FoldEnvKeys(m, NewBootstrap())

	app := m["APP"].(map[string]any)
	if _, left := app["PROBE"]; left {
		t.Errorf("掏空的词头键没清理：%v", app)
	}
	if got := app["probe_live_path"]; got != "/livez" {
		t.Errorf("probe_live_path = %v, want /livez（整棵树只有这一个候选，无需按大小写消歧）", got)
	}
	if got := app["probe_ready_path"]; got != "/readyz" {
		t.Errorf("probe_ready_path = %v, want /readyz", got)
	}
	if unknown := FindUnknownKeys(m, NewBootstrap()); len(unknown) != 0 {
		t.Errorf("折叠后仍有未知键 %q（env 注入姿势被判成笔误）", unknown)
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

// TestFoldEnvKeys_SiblingPrefixPairs 全契约扫描：同一词头下的兄弟字段一起经 env
// 注入时，每条分支必须各自折回自己的规范键。
//
// 这是「分支折叠」的判据，也是它的影响面清单——契约里这样的字段对是常态
// （probe_live_path/probe_ready_path、max_size/max_backups/max_age、
// auto_stream/auto_replay、access_key/access_secret …），按「整棵子树只有一条链
// 才折」判会一对都折不了：严格门禁报未知键，非严格 DiscardUnknown 静默丢掉。
// 扫描器随契约生长，新增兄弟字段自动进判据，不用手工维护清单。
func TestFoldEnvKeys_SiblingPrefixPairs(t *testing.T) {
	var sites []foldSite
	walkFoldSites(NewBootstrap().ProtoReflect().Descriptor(), nil, map[protoreflect.FullName]bool{}, &sites)
	if len(sites) == 0 {
		t.Fatal("契约里没扫到同词头兄弟字段——扫描器坏了，判据形同虚设")
	}
	pairs := 0
	for _, s := range sites {
		pairs += len(s.fields) * (len(s.fields) - 1) / 2
	}
	t.Logf("同词头兄弟字段注入点 %d 处、字段对 %d 对", len(sites), pairs)

	root := map[string]any{}
	want := make([]map[string]string, len(sites))
	for i, s := range sites {
		sub := map[string]any{}
		want[i] = map[string]string{}
		for j, fd := range s.fields {
			name := string(fd.Name())
			sentinel := fmt.Sprintf("v%d_%d", i, j)
			// env 编码：字段名去掉词头后，剩下的每段各成一层。
			setFoldLeaf(t, sub, strings.Split(strings.TrimPrefix(name, s.head+"_"), "_"), sentinel)
			want[i][name] = sentinel
		}
		graftFoldSite(root, s, sub)
	}

	FoldEnvKeys(root, NewBootstrap())

	for i, s := range sites {
		pm := foldSiteMap(t, root, s)
		if _, left := pm[s.head]; left {
			t.Errorf("%v：词头键 %q 没被清理（分支互相牵连）", s.path, s.head)
		}
		for name, sentinel := range want[i] {
			if got := pm[name]; got != sentinel {
				t.Errorf("%v：%s = %v，want %q", s.path, name, got, sentinel)
			}
		}
	}
	if unknown := FindUnknownKeys(root, NewBootstrap()); len(unknown) != 0 {
		t.Errorf("折叠后仍有未知键 %q", unknown)
	}
}

// TestFoldEnvKeys_SiblingPrefixValues 端到端：几组已发布字段一起经 env 注入，
// 断言落在 Unmarshal 后的契约值上——折错了名字会被 DiscardUnknown 静默丢掉，
// 只看树形状证明不了值到了该到的字段。optional bool 另判 presence：显式 false
// 与不表态在读侧同值，丢了 presence 就等于把「关掉」变成「保留实现默认」。
func TestFoldEnvKeys_SiblingPrefixValues(t *testing.T) {
	m := foldTree(map[string]any{
		"app.probe.live.path":      "/livez",
		"app.probe.ready.path":     "/readyz",
		"app.health.poll.interval": "3s",
		"server.sse.auto.stream":   "false",
		"server.sse.auto.replay":   "false",
	})
	m["logger"] = map[string]any{"backends": []any{map[string]any{
		"type": "slog",
		"slog": map[string]any{"rotate": map[string]any{
			"max": map[string]any{"size": "16", "backups": "3", "age": "7"},
		}},
		"aliyun": map[string]any{
			"access": map[string]any{"key": "AK", "secret": "SK"},
		},
	}}}
	FoldEnvKeys(m, NewBootstrap())

	var cfg bootstrapv1.BootstrapConfig
	if err := UnmarshalMap(m, &cfg); err != nil {
		t.Fatalf("UnmarshalMap: %v", err)
	}
	app := cfg.GetApp()
	if got := app.GetProbeLivePath(); got != "/livez" {
		t.Errorf("app.probe_live_path = %q, want /livez", got)
	}
	if got := app.GetProbeReadyPath(); got != "/readyz" {
		t.Errorf("app.probe_ready_path = %q, want /readyz", got)
	}
	if got := app.GetHealthPollInterval().AsDuration(); got != 3*time.Second {
		t.Errorf("app.health_poll_interval = %v, want 3s", got)
	}
	sse := cfg.GetServer().GetSse()
	if sse.AutoStream == nil || sse.GetAutoStream() {
		t.Errorf("server.sse.auto_stream = %v, want 显式 false", sse.AutoStream)
	}
	if sse.AutoReplay == nil || sse.GetAutoReplay() {
		t.Errorf("server.sse.auto_replay = %v, want 显式 false", sse.AutoReplay)
	}
	be := cfg.GetLogger().GetBackends()[0]
	rotate := be.GetSlog().GetRotate()
	if rotate.GetMaxSize() != 16 || rotate.GetMaxBackups() != 3 || rotate.GetMaxAge() != 7 {
		t.Errorf("slog.rotate = size %d backups %d age %d, want 16/3/7",
			rotate.GetMaxSize(), rotate.GetMaxBackups(), rotate.GetMaxAge())
	}
	aliyun := be.GetAliyun()
	if aliyun.GetAccessKey() != "AK" || aliyun.GetAccessSecret() != "SK" {
		t.Errorf("aliyun = key %q secret %q, want AK/SK", aliyun.GetAccessKey(), aliyun.GetAccessSecret())
	}
	if unknown := FindUnknownKeys(m, NewBootstrap()); len(unknown) != 0 {
		t.Errorf("折叠后仍有未知键 %q", unknown)
	}
}

// foldSeg 是注入路径的一段；list 表示该段是 repeated message，路径要经过一个
// 单元素切片。
type foldSeg struct {
	key  string
	list bool
}

// foldSite 是一处注入点：path 落到某个 message 的 map，head 是 env 编码切出的
// 词头，fields 是共用它的兄弟字段。
type foldSite struct {
	path   []foldSeg
	head   string
	fields []protoreflect.FieldDescriptor
}

// walkFoldSites 枚举契约里所有「同词头兄弟字段」注入点。
//
// 排除两类：map 字段（键由业务自持）；词头本身就是本层字段名（平级规范名优先，
// 这条 env 姿势表达不出来，不在折叠的射程内）。well-known types 整棵跳过
// （Struct/Value 既递归又不是契约面），seen 防契约自身的引用环。
func walkFoldSites(md protoreflect.MessageDescriptor, path []foldSeg,
	seen map[protoreflect.FullName]bool, out *[]foldSite) {
	if seen[md.FullName()] {
		return
	}
	seen[md.FullName()] = true

	fields := md.Fields()
	canonical := make(map[string]bool, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		canonical[string(fields.Get(i).Name())] = true
	}
	byHead := map[string][]protoreflect.FieldDescriptor{}
	var heads []string
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		head, _, multi := strings.Cut(string(fd.Name()), "_")
		if !multi || fd.IsMap() || canonical[head] {
			continue
		}
		if len(byHead[head]) == 0 {
			heads = append(heads, head)
		}
		byHead[head] = append(byHead[head], fd)
	}
	sort.Strings(heads)
	for _, head := range heads {
		if len(byHead[head]) >= 2 {
			*out = append(*out, foldSite{path: path, head: head, fields: byHead[head]})
		}
	}

	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		child := fd.Message()
		if fd.IsMap() || child == nil || strings.HasPrefix(string(child.FullName()), "google.protobuf.") {
			continue
		}
		// path 会被存进 site：append 复用底层数组会串改已记录的站点。
		sub := make([]foldSeg, len(path)+1)
		copy(sub, path)
		sub[len(path)] = foldSeg{key: string(fd.Name()), list: fd.IsList()}
		walkFoldSites(child, sub, seen, out)
	}
}

// setFoldLeaf 按 env 编码把剩余 token 逐段落成嵌套键。同一路径既是叶子又是中间
// 节点时直接判失败：那是契约自身的形状歧义，不该由扫描器悄悄覆盖掉一份。
func setFoldLeaf(t *testing.T, m map[string]any, parts []string, v any) {
	t.Helper()
	cur := m
	for i, p := range parts {
		last := i == len(parts)-1
		existing, exists := cur[p]
		if _, isMap := existing.(map[string]any); exists && isMap == last {
			t.Fatalf("注入路径冲突：%v（%q 处两种形状）", parts, p)
		}
		if last {
			cur[p] = v
			return
		}
		if !exists {
			cur[p] = map[string]any{}
		}
		cur = cur[p].(map[string]any)
	}
}

// graftFoldSite 把 site 的 env 形状子树挂到 root 的 path 上（路径段用规范键名，
// 折叠只在词头那一层发生）。
func graftFoldSite(root map[string]any, s foldSite, sub map[string]any) {
	cur := root
	for _, seg := range s.path {
		if seg.list {
			items, ok := cur[seg.key].([]any)
			if !ok {
				items = []any{map[string]any{}}
				cur[seg.key] = items
			}
			cur = items[0].(map[string]any)
			continue
		}
		next, ok := cur[seg.key].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[seg.key] = next
		}
		cur = next
	}
	cur[s.head] = sub
}

// foldSiteMap 沿 path 取到 site 所在 message 的 map。
func foldSiteMap(t *testing.T, root map[string]any, s foldSite) map[string]any {
	t.Helper()
	cur := root
	for _, seg := range s.path {
		if seg.list {
			cur = cur[seg.key].([]any)[0].(map[string]any)
			continue
		}
		cur = cur[seg.key].(map[string]any)
	}
	return cur
}
