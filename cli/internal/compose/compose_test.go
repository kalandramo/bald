package compose

// compose_test.go 是 `bald compose` 的验收测试：生成物确定、目录与 CI 同源、
// argv 装配可审计（--dry-run）、未知服务与占用文件各自给出可执行的修法。
//
// 不打桩 docker：所有断言都走 --dry-run 或纯函数，CI 不需要 docker 就能跑绿
// （需要真容器的场景由 integration 作业覆盖，那里本来就有真服务）。

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCmd 执行 compose 子命令，返回 stdout 与错误（弃掉 stderr 噪声）。
func runCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := NewCommand(Streams{In: strings.NewReader(""), Out: &out, ErrOut: &errOut})
	cmd.SetArgs(args)
	cmd.SilenceErrors = true
	err := cmd.Execute()
	return out.String(), err
}

// TestCatalogCoversIntegrationDeps 目录必须覆盖 CI integration 作业的五个后端：
// 本地与 CI 用同一批镜像，才能保证「本地绿」与「CI 绿」说的是同一件事。
func TestCatalogCoversIntegrationDeps(t *testing.T) {
	want := map[string]string{
		"etcd":   "quay.io/coreos/etcd:v3.5.14",
		"consul": "hashicorp/consul:1.19.2",
		"redis":  "redis:7-alpine",
		"mongo":  "mongo:7",
		"nacos":  "nacos/nacos-server:v2.3.2",
	}
	got := map[string]string{}
	for _, s := range catalog {
		got[s.Name] = s.Image
	}
	if len(got) != len(want) {
		t.Fatalf("catalog has %d services, want %d: %v", len(got), len(want), got)
	}
	for name, image := range want {
		if got[name] != image {
			t.Errorf("%s image = %q, want %q (must match .github/workflows/ci.yml services)", name, got[name], image)
		}
	}
}

// TestRenderYAML_DeterministicAndShaped 生成物逐字节确定，且关键形状与 CI 一致：
// 端口只绑 127.0.0.1、nacos 带 9848 与平台、项目名固定。
func TestRenderYAML_DeterministicAndShaped(t *testing.T) {
	first := renderYAML(catalog)
	if second := renderYAML(catalog); first != second {
		t.Fatal("renderYAML must be byte-identical across calls")
	}
	for _, want := range []string{
		"name: bald-infra",
		"  etcd:\n    image: quay.io/coreos/etcd:v3.5.14",
		"ETCD_LISTEN_CLIENT_URLS",
		"platform: linux/amd64",
		`"127.0.0.1:9848:9848"`,
		`MODE: "standalone"`,
		`NACOS_AUTH_ENABLE: "false"`,
	} {
		if !strings.Contains(first, want) {
			t.Errorf("compose definition missing %q:\n%s", want, first)
		}
	}
	// 端口不得裸绑（0.0.0.0 暴露到局域网）。
	for _, line := range strings.Split(first, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "ports:") {
			continue
		}
		if strings.Contains(trimmed, `"6379:6379"`) {
			t.Errorf("port must bind 127.0.0.1 explicitly: %s", trimmed)
		}
	}
}

// TestConfigStdoutAndOut --out 落盘；已存在即拒绝（不猜「要不要覆盖」）。
func TestConfigStdoutAndOut(t *testing.T) {
	out, err := runCmd(t, "config")
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if !strings.Contains(out, "name: bald-infra") {
		t.Errorf("config stdout must be the definition, got:\n%s", out)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	if _, err := runCmd(t, "config", "--out", path); err != nil {
		t.Fatalf("config --out: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	if string(raw) != renderYAML(catalog) {
		t.Error("--out must write exactly the rendered definition")
	}
	if _, err := runCmd(t, "config", "--out", path); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("config --out over an existing file must refuse, got: %v", err)
	}
}

// TestConfigServiceSubset --service 只渲染选中的服务（省掉不需要的后端）。
func TestConfigServiceSubset(t *testing.T) {
	out, err := runCmd(t, "config", "--service", "redis,etcd")
	if err != nil {
		t.Fatalf("config --service: %v", err)
	}
	if !strings.Contains(out, "  redis:") || !strings.Contains(out, "  etcd:") {
		t.Errorf("selected services missing:\n%s", out)
	}
	if strings.Contains(out, "  nacos:") {
		t.Errorf("unselected service must not be rendered:\n%s", out)
	}
}

// TestUpDryRunArgv argv 装配可审计：项目名、文件、-d、服务名齐全，且不带出
// 用户环境里的 compose.yaml（临时文件路径以 bald-compose- 开头）。
func TestUpDryRunArgv(t *testing.T) {
	out, err := runCmd(t, "up", "etcd", "redis", "--dry-run")
	if err != nil {
		t.Fatalf("up --dry-run: %v", err)
	}
	line := strings.TrimSpace(out)
	for _, want := range []string{
		"docker compose -f ", "bald-compose-", ".yaml",
		"-p bald-infra up -d etcd redis",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("argv missing %q, got: %s", want, line)
		}
	}
}

// TestUpDryRunAllServices 缺省全起：argv 里五个服务名都在（排序后）。
func TestUpDryRunAllServices(t *testing.T) {
	out, err := runCmd(t, "up", "--dry-run")
	if err != nil {
		t.Fatalf("up --dry-run: %v", err)
	}
	for _, name := range Services() {
		if !strings.Contains(out, name) {
			t.Errorf("default up must include %q: %s", name, out)
		}
	}
}

// TestDownDryRunUsesProjectScope down 只认项目名，不接服务名（compose 语义）。
func TestDownDryRunUsesProjectScope(t *testing.T) {
	out, err := runCmd(t, "down", "--dry-run")
	if err != nil {
		t.Fatalf("down --dry-run: %v", err)
	}
	if !strings.Contains(out, "-p bald-infra down --remove-orphans") {
		t.Errorf("down argv must scope by project: %s", out)
	}
	if _, err := runCmd(t, "down", "etcd"); err == nil {
		t.Error("down with a service name must be refused (compose down takes no service)")
	}
}

// TestStatusDryRun status 无参列全项目、带参只列选中服务。
func TestStatusDryRun(t *testing.T) {
	out, err := runCmd(t, "status", "--dry-run")
	if err != nil {
		t.Fatalf("status --dry-run: %v", err)
	}
	if !strings.Contains(out, "-p bald-infra ps") {
		t.Errorf("status argv must be `ps` without names: %s", out)
	}
	out, err = runCmd(t, "status", "mongo", "--dry-run")
	if err != nil {
		t.Fatalf("status mongo --dry-run: %v", err)
	}
	if !strings.Contains(out, "ps mongo") {
		t.Errorf("status argv must pass the selected service: %s", out)
	}
}

// TestFileFlagUsesUserCompose --file 指过去后 argv 用它，且文件不存在即报错
// （不打桩到不存在的路径上让 docker 去喊）。
func TestFileFlagUsesUserCompose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "my-compose.yaml")
	if err := os.WriteFile(path, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("seed compose file: %v", err)
	}
	out, err := runCmd(t, "up", "--file", path, "--dry-run")
	if err != nil {
		t.Fatalf("up --file: %v", err)
	}
	if !strings.Contains(out, path) {
		t.Errorf("argv must use the user compose file, got: %s", out)
	}
	if _, err := runCmd(t, "up", "--file", filepath.Join(dir, "nope.yaml"), "--dry-run"); err == nil {
		t.Error("missing --file must be refused before touching docker")
	}
}

// TestUnknownService 未知名给出可选清单（错误信息能直接改对）。
func TestUnknownService(t *testing.T) {
	_, err := runCmd(t, "up", "rabbitmq", "--dry-run")
	if err == nil {
		t.Fatal("unknown service must be refused")
	}
	for _, want := range []string{"unknown service", "rabbitmq", "etcd", "nacos"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must mention %q, got: %v", want, err)
		}
	}
}

// TestList 目录输出含名字/镜像/地址，且声明序稳定。
func TestList(t *testing.T) {
	out, err := runCmd(t, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.HasPrefix(out, "service\timage\tlocal address\n") {
		t.Errorf("list must start with a header, got:\n%s", out)
	}
	for _, s := range catalog {
		if !strings.Contains(out, s.Name+"\t"+s.Image+"\t"+s.Addr) {
			t.Errorf("list missing row for %s:\n%s", s.Name, out)
		}
	}
}
