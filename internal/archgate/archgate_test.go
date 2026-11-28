// Package archgate 是仓库原生的分层依赖门禁（S3，architecture-review-20261128 N-1）。
//
// 为什么在 check-arch.py 之外还要一份 Go 判定：go-gate 技能脚本住在开发者目录，
// CI 与 `go test ./...` 都够不着它；没有原生判据时，分层墙只对「装了技能的人」
// 成立。两份实现读同一份 .arch.json（唯一真相源），本包镜像其匹配语义
// （layer_of 最长前缀、glob_match 尾部 * 三段式），并额外做 check-arch 做不了的
// 变异测试——注入 bootstrap→transport/http 合成边必须判红，证明 N-1 规则不是
// 「现值 0 的静默通过」。
package archgate

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type forbidRule struct {
	From   string   `json:"from"`
	To     []string `json:"to"`
	Reason string   `json:"reason"`
}

type archConfig struct {
	Layers map[string][]string `json:"layers"`
	Allow  map[string][]string `json:"allow"`
	Forbid []forbidRule        `json:"forbid"`
	Ignore []string            `json:"ignore"`
	Strict struct {
		RequireLayerUnder []string `json:"require_layer_under"`
	} `json:"strict"`
}

type pkgNode struct {
	path    string
	imports []string
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil &&
			strings.TrimSpace(firstLine(string(b))) == "module github.com/kalandramo/bald" {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("找不到仓库根（go.mod module github.com/kalandramo/bald）")
		}
		dir = parent
	}
}

func inPrefix(p, prefix string) bool {
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

// layerPrefixMatch / layerOf / globMatch 与 go-gate check-arch.py 同名函数逐条对齐，
// 改动任何一条语义都必须同步另一份，否则两份门禁会给出不同判定。
func layerPrefixMatch(pkg, prefix string) bool {
	if !strings.Contains(prefix, "*") {
		return inPrefix(pkg, prefix)
	}
	psegs := strings.Split(pkg, "/")
	pat := strings.Split(prefix, "/")
	if len(psegs) < len(pat) {
		return false
	}
	for i, want := range pat {
		if want == "*" {
			continue
		}
		if psegs[i] != want {
			return false
		}
	}
	return true
}

func layerOf(pkg string, layers map[string][]string) string {
	best, bestLen := "", -1
	for name, prefixes := range layers {
		for _, prefix := range prefixes {
			if layerPrefixMatch(pkg, prefix) && len(prefix) > bestLen {
				best, bestLen = name, len(prefix)
			}
		}
	}
	return best
}

func globMatch(path, pattern string) bool {
	if strings.HasSuffix(pattern, "/*") {
		return inPrefix(path, strings.TrimSuffix(pattern, "/*"))
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(path, strings.TrimSuffix(pattern, "*"))
	}
	return inPrefix(path, pattern)
}

func loadContract(t *testing.T) (archConfig, string) {
	t.Helper()
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, ".arch.json"))
	if err != nil {
		t.Fatalf("读 .arch.json: %v", err)
	}
	var cfg archConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("解析 .arch.json: %v", err)
	}
	if len(cfg.Layers) == 0 {
		t.Fatal(".arch.json 缺 layers")
	}
	gm, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("读 go.mod: %v", err)
	}
	mod := ""
	for _, line := range strings.Split(string(gm), "\n") {
		if s := strings.TrimSpace(line); strings.HasPrefix(s, "module ") {
			mod = strings.Trim(strings.Fields(s)[1], `"`)
			break
		}
	}
	if mod == "" {
		t.Fatal("go.mod 缺 module 行")
	}
	norm := func(p string) string {
		if p == "" || p == mod || strings.HasPrefix(p, mod+"/") || strings.HasPrefix(p, "*") {
			return p
		}
		return mod + "/" + p
	}
	for name, prefixes := range cfg.Layers {
		for i, p := range prefixes {
			prefixes[i] = norm(p)
		}
		cfg.Layers[name] = prefixes
	}
	for i, rule := range cfg.Forbid {
		if _, isLayer := cfg.Layers[rule.From]; !isLayer {
			cfg.Forbid[i].From = norm(rule.From)
		}
	}
	for i, p := range cfg.Ignore {
		cfg.Ignore[i] = norm(p)
	}
	for i, p := range cfg.Strict.RequireLayerUnder {
		cfg.Strict.RequireLayerUnder[i] = norm(p)
	}
	for from, deps := range cfg.Allow {
		if _, ok := cfg.Layers[from]; !ok {
			t.Errorf("allow 引用了未定义的层: %s", from)
		}
		for _, dep := range deps {
			if _, ok := cfg.Layers[dep]; !ok {
				t.Errorf("allow.%s 引用了未定义的层: %s", from, dep)
			}
		}
	}
	return cfg, mod
}

func graph(t *testing.T, root string) []pkgNode {
	t.Helper()
	cmd := exec.Command("go", "list", "-f", "{{.ImportPath}}\t{{join .Imports \" \"}}", "./...")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var nodes []pkgNode
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		path, imports, _ := strings.Cut(line, "\t")
		nodes = append(nodes, pkgNode{path: path, imports: strings.Fields(imports)})
	}
	return nodes
}

type violation struct {
	pkg  string
	imp  string
	rule string
}

func (v violation) String() string {
	return v.pkg + " -> " + v.imp + " [" + v.rule + "]"
}

func evaluate(cfg archConfig, nodes []pkgNode) []violation {
	ignored := func(p string) bool {
		for _, prefix := range cfg.Ignore {
			if inPrefix(p, prefix) {
				return true
			}
		}
		return false
	}
	var out []violation
	for _, node := range nodes {
		if ignored(node.path) {
			continue
		}
		src := layerOf(node.path, cfg.Layers)
		if src == "" {
			for _, prefix := range cfg.Strict.RequireLayerUnder {
				if inPrefix(node.path, prefix) {
					out = append(out, violation{pkg: node.path, rule: "未归层包"})
				}
			}
			continue
		}
		allowed := map[string]bool{}
		for _, dep := range cfg.Allow[src] {
			allowed[dep] = true
		}
		for _, imp := range node.imports {
			if ignored(imp) {
				continue
			}
			tgt := layerOf(imp, cfg.Layers)
			if tgt != "" && tgt != src && !allowed[tgt] {
				out = append(out, violation{pkg: node.path, imp: imp,
					rule: "层依赖越界: " + src + " 不允许依赖 " + tgt})
				continue
			}
			for _, rule := range cfg.Forbid {
				_, isLayer := cfg.Layers[rule.From]
				hitFrom := rule.From == src
				if !isLayer {
					hitFrom = inPrefix(node.path, rule.From)
				}
				if !hitFrom {
					continue
				}
				for _, pat := range rule.To {
					if globMatch(imp, pat) {
						out = append(out, violation{pkg: node.path, imp: imp,
							rule: "显式禁止: " + rule.From + " ↛ " + pat})
						break
					}
				}
			}
		}
	}
	return out
}

// TestPackagesFullyLayered 镜像 check-arch --verify 的两条 FAIL 判据：
// 一包多命中（层前缀不相交）与未归层包（新包绕过分层判定）。
func TestPackagesFullyLayered(t *testing.T) {
	cfg, _ := loadContract(t)
	for _, node := range graph(t, repoRoot(t)) {
		var hits []string
		for name, prefixes := range cfg.Layers {
			for _, prefix := range prefixes {
				if layerPrefixMatch(node.path, prefix) {
					hits = append(hits, name)
					break
				}
			}
		}
		if len(hits) > 1 {
			t.Errorf("包 %s 同时命中多个层: %v（层前缀必须按目录子树互斥切分）", node.path, hits)
		}
		if len(hits) == 0 {
			t.Errorf("包 %s 不属于任何层（新包会绕过分层判定，须先归层）", node.path)
		}
	}
}

// TestNoLayerViolations 是全量 check 模式：allow 矩阵 + forbid 规则零违规。
func TestNoLayerViolations(t *testing.T) {
	cfg, _ := loadContract(t)
	for _, v := range evaluate(cfg, graph(t, repoRoot(t))) {
		t.Errorf("分层违规: %s", v)
	}
}

// TestContractNotVacuous 防空转：包图规模下限 + 每条 forbid 的 from 有源、
// 每个 to 至少命中全图一条真实 import 边。判据与 --verify 对齐（to 看全图，
// 不看该 from 出发的边），触发能力本身由变异测试补上。
func TestContractNotVacuous(t *testing.T) {
	cfg, _ := loadContract(t)
	nodes := graph(t, repoRoot(t))
	if len(nodes) < 140 {
		t.Fatalf("包图只有 %d 个包（下限 140）：go list 解析异常或模块拓扑回潮，分层判定不可信", len(nodes))
	}
	allImports := map[string]bool{}
	live := map[string]bool{}
	for _, node := range nodes {
		live[node.path] = true
		for _, imp := range node.imports {
			allImports[imp] = true
		}
	}
	for _, rule := range cfg.Forbid {
		_, isLayer := cfg.Layers[rule.From]
		srcFound := false
		for p := range live {
			if (isLayer && layerOf(p, cfg.Layers) == rule.From) || (!isLayer && inPrefix(p, rule.From)) {
				srcFound = true
				break
			}
		}
		if !srcFound {
			t.Errorf("forbid from=%s 匹配不到任何包（规则永不触发）", rule.From)
		}
		for _, pat := range rule.To {
			found := false
			for imp := range allImports {
				if globMatch(imp, pat) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("forbid to=%s 在现有 import 里不存在（路径写错或被清理后失效）", pat)
			}
		}
	}
}

// TestMutationKernelToConcreteTransportFails 是 S3 的明文判据（architecture-review
// N-1）：向内存图注入合成边 bootstrap → transport/http、transport/grpc、
// pkg/appkit，门禁必须判红并点名对应规则。若哪天有人把这三条边之一写回真实
// 代码，TestNoLayerViolations 会以同样的规则红——本测试钉的是规则没有空转。
func TestMutationKernelToConcreteTransportFails(t *testing.T) {
	cfg, mod := loadContract(t)
	nodes := graph(t, repoRoot(t))
	mutated := make([]pkgNode, len(nodes))
	copy(mutated, nodes)
	for i, node := range mutated {
		if node.path == mod+"/bootstrap" {
			mutated[i].imports = append(append([]string{}, node.imports...),
				mod+"/transport/http", mod+"/transport/grpc", mod+"/pkg/appkit")
		}
	}
	if vs := evaluate(cfg, nodes); len(vs) != 0 {
		t.Fatalf("基线图应当零违规，实际 %d 条: %v", len(vs), vs)
	}
	vs := evaluate(cfg, mutated)
	wanted := map[string]string{
		mod + "/transport/http": "显式禁止: kernel",
		mod + "/transport/grpc": "显式禁止: kernel",
		mod + "/pkg/appkit":     "层依赖越界: kernel 不允许依赖 engine",
	}
	got := map[string]string{}
	for _, v := range vs {
		if v.pkg == mod+"/bootstrap" {
			got[v.imp] = v.rule
		}
	}
	for imp, wantRule := range wanted {
		if !strings.Contains(got[imp], wantRule) {
			t.Errorf("变异边 bootstrap -> %s 未被判红或规则名不对：want 含 %q, got %q", imp, wantRule, got[imp])
		}
	}
}
