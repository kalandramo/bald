package bconf

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// TestGeneratedMatchesProto 守住契约层生成管线：用本地 buf + protoc-gen-go
// 按 buf.gen.local.yaml 重新生成，必须与入库的 gen/go 逐字节一致。
// 工具链缺失时跳过（CI 不必装 buf）；工具链在场则先核对两把 pin，再做字节比对：
//   - buf 版本 == bconf/buf.version（驱动器的注释排版会进产物字节）
//   - protoc-gen-go 版本 == 入库产物头部声明的版本
//
// 两条判据都在字节比对之前单独判红，是为了让「红」一眼分得清契约漂移还是工具链
// 换代（口径见风险登记册 §7.9）。
func TestGeneratedMatchesProto(t *testing.T) {
	if _, err := exec.LookPath("buf"); err != nil {
		t.Skip("buf 不在 PATH，跳过契约层生成校验")
	}
	if _, err := exec.LookPath("protoc-gen-go"); err != nil {
		t.Skip("protoc-gen-go 不在 PATH，跳过契约层生成校验")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("定位测试源文件失败")
	}
	root := filepath.Dir(file)

	pinned := strings.TrimSpace(string(mustRead(t, filepath.Join(root, "buf.version"))))
	if pinned == "" {
		t.Fatal("bconf/buf.version 是空的——pin 文件是这条腿的前提，不能留空")
	}
	if got := toolVersion(t, "buf"); got != pinned {
		t.Fatalf("本机 buf=%s，而 bconf/buf.version pin 的是 %s。这条腿判的是产物**字节**，buf 的注释排版会进字节（实测 1.50.0 与入库产物零差异、1.57.2 给中文注释的 `//` 补一个空格），所以版本不符时的红只证明工具链，不证明契约。用 pin 版本复跑；要升级版本，就把 buf.version 与用新版本重生成的 gen/go 一起提交，让 diff 自己说话。", got, pinned)
	}

	out := t.TempDir()

	want := filepath.Join(root, "gen", "go")
	got := filepath.Join(out, "gen", "go")
	wantFiles := readGenTree(t, want)
	// 生成树为空时逐字节比对会「零差异」通过，所以先钉住它确实有产物。
	if len(wantFiles) == 0 {
		t.Fatalf("%s 下没有任何生成物——这条腿在比一个空树，判据失效", want)
	}

	// protoc-gen-go 的版本行就在产物头部注释里，所以「产物声明的版本」即单一真相；
	// 本机插件版本不符时红的是插件换代，而不是 proto 契约漂移。
	pluginWant := pluginVersion(t, wantFiles)
	if pluginGot := toolVersion(t, "protoc-gen-go"); pluginGot != pluginWant {
		t.Fatalf("本机 protoc-gen-go=%s，而入库产物头部声明的是 v%s。插件版本会进产物字节（版本行本身），所以这条红只证明生成器换代，不证明契约漂移。要么装 v%s 复跑，要么用 v%s 重新生成并把产物一起提交。",
			pluginGot, pluginWant, pluginWant, pluginGot)
	}

	cmd := exec.Command("buf", "generate", "--template", "buf.gen.local.yaml", "-o", out)
	cmd.Dir = root
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("buf generate 失败: %v\n%s", err, b)
	}

	gotFiles := readGenTree(t, got)

	var problems []string
	for _, p := range unionKeys(wantFiles, gotFiles) {
		w, inWant := wantFiles[p]
		g, inGot := gotFiles[p]
		switch {
		case !inGot:
			problems = append(problems, "缺失: "+p)
		case !inWant:
			problems = append(problems, "多出: "+p)
		case !bytes.Equal(w, g):
			problems = append(problems, "内容不一致: "+p)
		}
	}
	if len(problems) > 0 {
		// 到这里 buf 版本已核对等于 pin，所以红的是契约本身。插件版本也打进文案：
		// protoc-gen-go 的版本行就在产物字节里，插件换版同样会表现为「内容不一致」。
		t.Fatalf("契约层生成物与 proto 不一致（%d 项，前 10 项）：\n  %s\n本机 buf=%s（= bconf/buf.version 的 pin），protoc-gen-go=%s，因此这是契约漂移而不是工具链差异。\n确认是契约变更后用 `buf generate --template buf.gen.local.yaml` 重新生成并提交；要升级 buf，就把 buf.version 与用新版本重生成的 gen/go 一起提交（口径见风险登记册 §7.9）",
			len(problems), joinLimited(problems, 10), pinned, toolVersion(t, "protoc-gen-go"))
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s: %v", path, err)
	}
	return b
}

// toolVersion 取 `--version` 首行的最后一个字段并去掉前导 v，
// 因为 buf 打印裸版本号、protoc-gen-go 打印 "protoc-gen-go v1.36.12"。
func toolVersion(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.Command(name, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("%s --version 失败: %v\n%s", name, err, out)
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	fields := strings.Fields(line)
	if len(fields) == 0 {
		t.Fatalf("%s --version 输出为空", name)
	}
	return strings.TrimPrefix(fields[len(fields)-1], "v")
}

// pluginVersion 从入库产物的头部注释里读出生成器版本。
// 取排序后第一个 .pb.go，保证同一份产物在任何机器上读出同一个数字。
func pluginVersion(t *testing.T, files map[string][]byte) string {
	t.Helper()
	for _, p := range unionKeys(files, nil) {
		if !strings.HasSuffix(p, ".pb.go") {
			continue
		}
		for _, line := range strings.SplitN(string(files[p]), "\n", 8) {
			fields := strings.Fields(line)
			for i, f := range fields {
				if f == "protoc-gen-go" && i+1 < len(fields) {
					return strings.TrimPrefix(fields[i+1], "v")
				}
			}
		}
		t.Fatalf("%s 的头部注释里没有 protoc-gen-go 版本行——无法核对生成器 pin", p)
	}
	t.Fatal("生成树里没有 .pb.go 文件")
	return ""
}

func readGenTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		t.Fatalf("读取生成树 %s: %v", root, err)
	}
	return out
}

func unionKeys(a, b map[string][]byte) []string {
	seen := make(map[string]bool, len(a)+len(b))
	keys := make([]string, 0, len(a)+len(b))
	for k := range a {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for k := range b {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func joinLimited(items []string, n int) string {
	if len(items) > n {
		items = items[:n]
	}
	return strings.Join(items, "\n  ")
}
