package config

import (
	"os"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// --- env 层下划线字段（合树前折回规范键）---

// TestEnv_UnderscoreContractField 锁住 env 表达含下划线契约字段的能力。
//
// 契约：environMap 把 env 名的下划线当点路径分隔符，而契约字段名本身
// 可能含下划线（如 server_addrs）。envLayer 必须在合树前用 bconf.FoldEnvKeys
// 把切出的嵌套链折回规范键，否则该键与文件层的规范键不同名（env 覆盖失效），
// 开着严格门禁还会被当成笔误。本测试即该契约的回归锁。
func TestEnv_UnderscoreContractField(t *testing.T) {
	t.Setenv("BALD_ADMIN_CONFIG_NACOS_SERVER_ADDRS", "10.0.0.1:8848")
	t.Setenv("BALD_ADMIN_CONFIG_NACOS_DATA_ID", "app.yaml")
	t.Setenv("BALD_ADMIN_CONFIG_NACOS_NAMESPACE", "ns-1")
	// 对照：无下划线字段（既有能力，不得回归）
	t.Setenv("BALD_ADMIN_SERVER_HTTP_ADDR", ":8080")

	s, err := Load(Options{Name: "bald-admin"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer s.Close()

	var cfg bootstrapv1.BootstrapConfig
	if err := s.Unmarshal(&cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	n := cfg.GetConfig().GetNacos()
	if n == nil {
		t.Fatal("config.nacos 段未构造（env 未生效）")
	}
	if got := n.GetServerAddrs(); len(got) != 1 || got[0] != "10.0.0.1:8848" {
		t.Errorf("nacos.server_addrs = %v, want [10.0.0.1:8848]", got)
	}
	if got := n.GetDataId(); got != "app.yaml" {
		t.Errorf("nacos.data_id = %q, want app.yaml", got)
	}
	if got := n.GetNamespace(); got != "ns-1" {
		t.Errorf("nacos.namespace = %q, want ns-1", got)
	}
	// 回归：无下划线路径不受折叠影响
	if got := cfg.GetServer().GetHttp().GetAddr(); got != ":8080" {
		t.Errorf("server.http.addr = %q, want :8080", got)
	}
}

// TestEnv_UnderscoreMultiWord 验证多下划线字段（a_b_c）逐层折叠。
func TestEnv_UnderscoreMultiWord(t *testing.T) {
	t.Setenv("BALD_ADMIN_DATABASE_SQL_MAX_OPEN_CONNECTIONS", "42")
	t.Setenv("BALD_ADMIN_DATABASE_SQL_ENABLE_TRACE", "true")

	s, err := Load(Options{Name: "bald-admin"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer s.Close()

	var cfg bootstrapv1.BootstrapConfig
	if err := s.Unmarshal(&cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	sql := cfg.GetDatabase().GetSql()
	if sql == nil {
		t.Fatal("database.sql 段未构造")
	}
	if got := sql.GetMaxOpenConnections(); got != 42 {
		t.Errorf("database.sql.max_open_connections = %v, want 42", got)
	}
	if !sql.GetEnableTrace() {
		t.Error("database.sql.enable_trace = false, want true")
	}
}

// TestEnv_UnderscoreNoFalsePositive 反证：折叠不得把真嵌套误当字段名。
// {app: {log: {...}}} 形态下，若契约无 app_log 字段，应走未知键路径
// （顶层 app 是契约字段，其下 log 不是——属嵌套未知）。
func TestEnv_UnderscoreNoFalsePositive(t *testing.T) {
	settings := map[string]any{
		"app": map[string]any{"log": map[string]any{"level": "debug"}},
	}
	cfg := &bootstrapv1.BootstrapConfig{}
	// app 是契约字段，app.log 不是——折叠不得凭空造出 app_log
	if err := Unmarshal(settings, cfg); err == nil {
		// 若未启用严格模式，至少不得把 level 折进任何字段
		t.Log("未启用严格模式：app.log 被静默忽略（可接受）")
	}
	if got := cfg.GetApp().GetName(); got != "" {
		t.Errorf("app.name 不应被 app.log.level 污染，实际 %q", got)
	}
}

// --- 契约段未知键 fail-fast（opt-in 严格模式）---

// TestStrict_UnknownContractKeyFails 锁住「契约段写错键名 → 报错」。
// 这是项目退役 viper 的原始动因（写错键名静默落到零值），env 路径需同等保障。
func TestStrict_UnknownContractKeyFails(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll("configs", 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "server:\n  htpp: { addr: \":8080\" }\n" // 笔误：http → htpp
	if err := os.WriteFile("configs/bald-admin.yaml", []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(Options{
		Name:           "bald-admin",
		ConfigFile:     "configs/bald-admin.yaml",
		StrictContract: true,
	})
	if err == nil {
		t.Fatal("契约段未知键 server.htpp 应报错，实际静默通过")
	}
	if !strings.Contains(err.Error(), "htpp") {
		t.Errorf("错误信息应指名未知键 htpp，实际: %v", err)
	}
}

// TestStrict_BusinessSectionAllowed 反证：业务自定义段必须放行（白名单）。
// 设计文档明写：proto 只约束框架级配置，业务段自行解析。
func TestStrict_BusinessSectionAllowed(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll("configs", 0o755); err != nil {
		t.Fatal(err)
	}
	yml := `server:
  http: { addr: ":8080" }
login:
  rate_limit: { rate: 1 }
file:
  bucket: my-bucket
`
	if err := os.WriteFile("configs/bald-admin.yaml", []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(Options{
		Name:                  "bald-admin",
		ConfigFile:            "configs/bald-admin.yaml",
		StrictContract:        true,
		AllowBusinessSections: []string{"login", "file"},
	})
	if err != nil {
		t.Fatalf("业务段经白名单应放行，实际报错: %v", err)
	}
}

// TestStrict_NestedUnknownKeyFails 锁住嵌套层级的未知键。
func TestStrict_NestedUnknownKeyFails(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll("configs", 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "server:\n  http: { addrr: \":8080\" }\n" // 笔误：addr → addrr
	if err := os.WriteFile("configs/bald-admin.yaml", []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(Options{
		Name:           "bald-admin",
		ConfigFile:     "configs/bald-admin.yaml",
		StrictContract: true,
	})
	if err == nil {
		t.Fatal("嵌套未知键 server.http.addrr 应报错，实际静默通过")
	}
	if !strings.Contains(err.Error(), "addrr") {
		t.Errorf("错误信息应指名未知键 addrr，实际: %v", err)
	}
}

var _ = os.Getenv

// --- env 多词键在合树前折回规范名（§7.7）---
//
// environMap 把下划线一律当路径分隔符，因此多词契约字段（rate_limit /
// max_open_connections …）在 env 里落树成嵌套链。链必须在**合并之前**折回
// 规范键（envLayer → bconf.FoldEnvKeys）：合树之后再折来不及——读侧的
// 「平级匹配优先」会让 env 的折叠链永远排在文件层的规范键之后，env > file
// 静默失效；严格门禁的未知键扫描没有折叠兜底，会把这条合法注入判成笔误。
// 下面三条各钉一个后果。

// TestEnv_MultiWordBeatsFile 钉分层优先级：文件写规范键、env 写下划线名，
// env 必须赢。
func TestEnv_MultiWordBeatsFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll("configs", 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "server:\n  http:\n    middleware:\n      rate_limit:\n        burst: 3\n"
	if err := os.WriteFile("configs/bald-admin.yaml", []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BALD_ADMIN_SERVER_HTTP_MIDDLEWARE_RATE_LIMIT_BURST", "7")

	s, err := Load(Options{Name: "bald-admin", ConfigFile: "configs/bald-admin.yaml"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer s.Close()

	var cfg bootstrapv1.BootstrapConfig
	if err := s.Unmarshal(&cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := cfg.GetServer().GetHttp().GetMiddleware().GetRateLimit().GetBurst(); got != 7 {
		t.Errorf("server.http.middleware.rate_limit.burst = %d, want 7（env 覆盖文件；=3 说明 env 的折叠链输给了文件层的规范键）", got)
	}
}

// TestEnv_MultiWordPassesStrictGate 钉严格门禁：多词键的 env 注入姿势不是笔误，
// 开着 StrictContract 也必须放行。
func TestEnv_MultiWordPassesStrictGate(t *testing.T) {
	t.Setenv("BALD_ADMIN_SERVER_HTTP_MIDDLEWARE_TIMEOUT_DEFAULT_TIMEOUT_MS", "50")
	t.Setenv("BALD_ADMIN_DATABASE_SQL_MAX_OPEN_CONNECTIONS", "64")

	s, err := Load(Options{Name: "bald-admin", StrictContract: true})
	if err != nil {
		t.Fatalf("Load 拒绝了合法的 env 多词键: %v", err)
	}
	defer s.Close()

	var cfg bootstrapv1.BootstrapConfig
	if err := s.Unmarshal(&cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := cfg.GetServer().GetHttp().GetMiddleware().GetTimeout().GetDefaultTimeoutMs(); got != 50 {
		t.Errorf("timeout.default_timeout_ms = %d, want 50", got)
	}
	if got := cfg.GetDatabase().GetSql().GetMaxOpenConnections(); got != 64 {
		t.Errorf("sql.max_open_connections = %d, want 64", got)
	}
}

// TestEnv_MultiWordTypoStillFailsGate 反证：折叠不是「凡带下划线都放行」——
// 折不出契约字段名的链照样要被门禁指名，且指名的是**没折动的那条链**
// （门禁报的是链头，整棵子树一次计入）。
func TestEnv_MultiWordTypoStillFailsGate(t *testing.T) {
	t.Setenv("BALD_ADMIN_SERVER_HTTP_MIDDLEWARE_RAT_LIMIT_BURST", "7")

	_, err := Load(Options{Name: "bald-admin", StrictContract: true})
	if err == nil {
		t.Fatal("拼错的 env 键（rate_limit → rat_limit）被放行，折叠把门禁变成了摆设")
	}
	const wantKey = ".server.http.middleware.rat"
	if !strings.Contains(err.Error(), wantKey) {
		t.Errorf("错误应指名未折叠的链 %q，实际: %v", wantKey, err)
	}
}

// TestEnv_MountEnvFoldsToo 钉两遍合并的第二遍：DeferEnv 下由 MountEnv 挂载的
// env 层必须走同一条折叠路径（漏了它，DeferEnv 的角色仍会撞上老问题）。
// 对照文件层写同一字段：只验 env 单独生效的话，读侧的折叠兜底会把漏折掩盖掉。
func TestEnv_MountEnvFoldsToo(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll("configs", 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "server:\n  http:\n    middleware:\n      rate_limit:\n        burst: 3\n"
	if err := os.WriteFile("configs/bald-admin.yaml", []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BALD_ADMIN_SERVER_HTTP_MIDDLEWARE_RATE_LIMIT_BURST", "7")

	s, err := Load(Options{Name: "bald-admin", ConfigFile: "configs/bald-admin.yaml", DeferEnv: true})
	if err != nil {
		t.Fatalf("Load(DeferEnv): %v", err)
	}
	defer s.Close()

	s.MountEnv("bald-admin")

	var cfg bootstrapv1.BootstrapConfig
	if err := s.Unmarshal(&cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := cfg.GetServer().GetHttp().GetMiddleware().GetRateLimit().GetBurst(); got != 7 {
		t.Errorf("MountEnv 后 rate_limit.burst = %d, want 7（=3 说明第二遍没折，env 又输给了文件层）", got)
	}
}
