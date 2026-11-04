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
// 工具链缺失时跳过（CI 不必装 buf）；工具链在场则先核对三把 pin，再做字节比对：
//   - buf 版本 == bconf/buf.version
//   - protoc-gen-go 的 module 版本 == 入库产物头部声明的版本
//   - 编译 protoc-gen-go 用的 Go（major.minor）== bconf/protoc-gen-go.goversion
//
// 三条判据都在字节比对之前单独判红，是为了让「红」一眼分得清契约漂移还是工具链
// 换代（口径见风险登记册 §7.9）。
//
// 第三条是 2026-11-04 实测补的，它推翻了对前两轴的归因，值得把证据留在原地：
//   - 同一把插件（自报 v1.36.12、`go version -m` 里 module h1 也相同），编译它的 Go
//     是 go1.26.5 时，产物把 `bootstrap/v1/log.pb.go` 的字段注释 `//（…` 排成
//     `// （…`（多一个空格）；由 go1.27.1 与 go1.27.3 编译的两把都与入库产物零差异。
//     机制在 protobuf 的 compiler/protogen：`(*GeneratedFile).Content()` 用 stdlib
//     go/parser + go/printer 重排产物，注释排版口味跟着**编译插件时用的那把 Go**走。
//   - buf 的版本号对字节是**中性**的（实测 1.50.0 与 1.57.2 配同一把插件，整树零差异）。
//     pin 它仍然保留，但理由换成「这条腿只在这套登记过的组合上被验证过」，不是
//     「buf 会改排版」——原先那句是 PATH 解析串了插件后得出的误判。
//   - 把 Go 钉住换 module 版本才是插件换代真的进字节：v1.36.10（同样由 go1.27.1 编译）
//     与入库产物差在**全部 18 个 .pb.go**（头部版本行等），而那一行中文注释反而不变。
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
		t.Fatalf("本机 buf=%s，而 bconf/buf.version pin 的是 %s。这条腿判的是产物**字节**；注释文本由 buf 读 proto 后以 source_code_info 投递给插件，所以 buf 换代**可能**改排版——只是实测这两个版本（1.50.0 与 1.57.2 配同一把插件）整树零差异。判据只在这套登记过的组合上被验证过，所以版本不符时先换 pin 版本复跑取证，别直接读成契约漂移；要升级，就把 buf.version 与用新版本重生成的 gen/go 一起提交，让 diff 自己说话。", got, pinned)
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
		t.Fatalf("本机 protoc-gen-go=%s，而入库产物头部声明的是 v%s。插件版本会进产物字节（版本行本身；实测同一把 Go 编译的 v1.36.10 与 v1.36.12 差在全部 18 个 .pb.go），所以这条红只证明生成器换代，不证明契约漂移。要么装 v%s 复跑，要么用 v%s 重新生成并把产物一起提交。",
			pluginGot, pluginWant, pluginWant, pluginGot)
	}

	// 第三轴：插件是**用哪把 Go 编译的**。它不在 `protoc-gen-go --version` 里，也不在
	// 产物字节里（产物只写 module 版本），却是注释排版的答案——所以单独读构建信息。
	pinGo := strings.TrimSpace(string(mustRead(t, filepath.Join(root, "protoc-gen-go.goversion"))))
	if pinGo == "" {
		t.Fatal("bconf/protoc-gen-go.goversion 是空的——这条轴没有 pin 就无从判红")
	}
	if gotGo := pluginBuildGo(t); gotGo != pinGo {
		t.Fatalf("本机 protoc-gen-go 由 go%s 编译，而 bconf/protoc-gen-go.goversion pin 的是 go%s。编译插件的 Go 会经 protobuf 的 protogen（Content() 用 stdlib go/printer 重排产物）进产物字节：实测 go1.26.5 那把把中文注释 `//（…` 排成 `// （…`，go%s 那把与入库产物零差异。所以这条红只证明工具链口味，不证明契约。补一把合规的：`go install google.golang.org/protobuf/cmd/protoc-gen-go@v%s`（用 go%s 或该 pin 口味实测一致的补丁版）；要换口味，就把 goversion 与用新插件重生成的 gen/go 一起提交（口径见风险登记册 §7.9）",
			gotGo, pinGo, pinGo, pluginWant, pinGo)
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
		// 到这里三把 pin 都已等于本机（buf 版本、插件 module 版本、编译插件的 Go
		// major.minor），所以这里的红才有「契约」含义。留一条退路：pin 是 major.minor
		// 粒度，同一 minor 的补丁位实测无关（go1.27.1 与 go1.27.3 同为零差异），但
		// **更旧的口味也可能换个补丁位复现**——所以文案仍要求复跑一次，只是复跑的对象
		// 换成「pin 那套工具链」而不是「另一个版本的同一把工具」。
		t.Fatalf("契约层生成物与 proto 不一致（%d 项，前 10 项）：\n  %s\n本机 buf=%s（= bconf/buf.version 的 pin），protoc-gen-go=v%s（= 入库产物头部声明），编译它的 Go=go%s（= bconf/protoc-gen-go.goversion 的 pin）。\n三把 pin 都对上还差字节，就是契约真的变了：用 `buf generate --template buf.gen.local.yaml` 重生成并把 gen/go 与 proto 改动一起提交；若怀疑是本机工具链的补丁位口味，先按 §7.9 的矩阵换一把 pin 组合的插件复跑再判",
			len(problems), joinLimited(problems, 10), pinned, pluginWant, pluginBuildGo(t))
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

// pluginBuildGo 报出「编译本机 protoc-gen-go 用的 Go」的 major.minor。
// 这一轴在插件自己的 `--version` 里看不见（那只报 protobuf 的 module 版本），
// 而产物字节恰恰跟着它走，所以从二进制的构建信息里读：`go version -m <插件>` 首行
// 形如 `/path/protoc-gen-go: go1.27.1`。补丁位实测无关（go1.27.1 与 go1.27.3 编译的
// 同一版插件都与入库产物零差异），故只取到 major.minor。
// go 命令缺席时跳过——与 buf/插件缺席同一类（环境缺失，不是判据失败）。
func pluginBuildGo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go 不在 PATH，读不到 protoc-gen-go 的构建信息，跳过契约层生成校验")
	}
	path, err := exec.LookPath("protoc-gen-go")
	if err != nil {
		t.Fatalf("protoc-gen-go 不在 PATH: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	out, err := exec.Command("go", "version", "-m", path).CombinedOutput()
	if err != nil {
		t.Fatalf("`go version -m %s` 失败: %v\n%s", path, err, out)
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	fields := strings.Fields(line)
	if len(fields) == 0 || !strings.HasPrefix(fields[len(fields)-1], "go") {
		t.Fatalf("`go version -m %s` 首行读不出 Go 版本: %q", path, line)
	}
	v := strings.TrimPrefix(fields[len(fields)-1], "go")
	if parts := strings.Split(v, "."); len(parts) >= 2 {
		v = strings.Join(parts[:2], ".")
	}
	return v
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
