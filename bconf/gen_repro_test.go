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
// proto 改了没重生成，或本地插件版本与远端 pin 漂移。
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
		t.Fatalf("契约层生成物与 proto 不一致（%d 项，前 10 项）：\n  %s\n用 `buf generate --template buf.gen.local.yaml` 重新生成并提交",
			len(problems), joinLimited(problems, 10))
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
