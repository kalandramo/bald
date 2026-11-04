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
// 工具链缺失时跳过（CI 不必装 buf）；工具链在场却不一致则失败——这说明
// proto 改了没重生成，或本机 buf/插件版本与产出入库产物那一版不同（buf 的
// 注释排版会进产物字节，见失败文案与风险登记册 §7.9）。
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
	out := t.TempDir()

	cmd := exec.Command("buf", "generate", "--template", "buf.gen.local.yaml", "-o", out)
	cmd.Dir = root
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("buf generate 失败: %v\n%s", err, b)
	}

	want := filepath.Join(root, "gen", "go")
	got := filepath.Join(out, "gen", "go")
	wantFiles := readGenTree(t, want)
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
		// 字节判据绑 buf 版本的注释排版形态：1.50.0 与入库产物零差异，1.57.2 会给
		// 中文注释的 `//` 补一个空格（bootstrap/v1/log.pb.go），于是「没改 proto 也红」。
		// 把版本打进失败文案，是为了让这条红一眼分得清「契约漂了」还是「工具链变了」。
		ver, verr := exec.Command("buf", "--version").CombinedOutput()
		bufVersion := "未知"
		if verr == nil {
			bufVersion = strings.TrimSpace(string(ver))
		}
		t.Fatalf("契约层生成物与 proto 不一致（%d 项，前 10 项）：\n  %s\n本机 buf=%s；与入库产物一致的版本实测是 1.50.0（风险登记册 §7.9）——版本不同先换回 1.50.0 复跑，仍然不一致才是契约漂移。\n确认是契约变更后用 `buf generate --template buf.gen.local.yaml` 重新生成并提交",
			len(problems), joinLimited(problems, 10), bufVersion)
	}
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
