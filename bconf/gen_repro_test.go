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

// TestGeneratedMatchesProto 守住契约层生成管线：在 bconf module 里**自编译**
// protoc-gen-go，用它按 buf.gen.local.yaml 重新生成，必须与入库的 gen/go 逐字节一致。
// buf 缺席时跳过（环境缺失，不是判据失败）；buf 在场时判三件事，都在字节比对之前
// 单独判红，所以红的时候一眼分得清契约漂移还是工具链换代（口径见风险登记册 §7.9）：
//   - buf 版本 == bconf/buf.version（唯一还读 PATH 的一轴，实测对字节中性）
//   - 自编译插件的 module 版本 == 入库产物头部声明的版本（版本轴读 **bconf/go.mod**）
//   - 编译该插件用的 Go（major.minor）== bconf/protoc-gen-go.goversion
//
// 后两轴以前是读 PATH 上那把插件的（`protoc-gen-go --version` 加 `go version -m`），
// 改成自编译之后，插件版本由 go.mod 决定、编译口味由跑这条测试的工具链决定，于是
// 「本机 PATH 里那把插件是哪年装的」不再能冒充判据通过或判红——§7.9 前四轮的归因
// 全都栽在这上面。代价是这条腿要能 `go build`（包源就在模块缓存里，离线也行）。
//
// 三条判据各自的历史证据（实测，不是猜测）：
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
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go 不在 PATH，无法自编译 protoc-gen-go，跳过契约层生成校验")
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

	// 这条腿唯一可信的那把插件：在 bconf module 里现编，版本轴因此由 go.mod 决定。
	plugin := selfBuiltPlugin(t, root)

	out := t.TempDir()

	want := filepath.Join(root, "gen", "go")
	got := filepath.Join(out, "gen", "go")
	wantFiles := readGenTree(t, want)
	// 生成树为空时逐字节比对会「零差异」通过，所以先钉住它确实有产物。
	if len(wantFiles) == 0 {
		t.Fatalf("%s 下没有任何生成物——这条腿在比一个空树，判据失效", want)
	}

	// 版本轴：自编译用的是 bconf/go.mod 里 require 的那个 protobuf，而「产物声明的版本」
	// 是入库字节里的头部行。两者不符 ⇒ go.mod 与产物换代不同步；本机 PATH 上装过什么
	// 插件不参与这一判据（这一轴以前会被 PATH 冒充）。
	pluginWant := pluginVersion(t, wantFiles)
	if pluginGot := toolVersion(t, plugin); pluginGot != pluginWant {
		t.Fatalf("bconf/go.mod 的 protobuf 编出的插件是 v%s，而入库产物头部声明的是 v%s。插件版本会进产物字节（版本行本身；实测同一把 Go 编译的 v1.36.10 与 v1.36.12 差在全部 18 个 .pb.go），所以这条红只证明生成器换代，不证明契约漂移。要么把 go.mod 的 require 升到 v%s 复跑，要么用 v%s 重新生成 gen/go 并把产物一起提交。",
			pluginGot, pluginWant, pluginWant, pluginGot)
	}

	// 第三轴：插件是**用哪把 Go 编译的**。它不在 `protoc-gen-go --version` 里，也不在
	// 产物字节里（产物只写 module 版本），却是注释排版的答案——所以读刚编出那把的构建信息
	// （自编译后这一轴就是「跑这条测试的工具链」，GOTOOLCHAIN 换出的那把也照实读）。
	pinGo := strings.TrimSpace(string(mustRead(t, filepath.Join(root, "protoc-gen-go.goversion"))))
	if pinGo == "" {
		t.Fatal("bconf/protoc-gen-go.goversion 是空的——这条轴没有 pin 就无从判红")
	}
	if gotGo := goVersionOfBinary(t, plugin); gotGo != pinGo {
		t.Fatalf("刚编出的 protoc-gen-go 由 go%s 编译，而 bconf/protoc-gen-go.goversion pin 的是 go%s。编译插件的 Go 会经 protobuf 的 protogen（Content() 用 stdlib go/printer 重排产物）进产物字节：实测 go1.26.5 那把把中文注释 `//（…` 排成 `// （…`，而 go1.27.1 与 go1.27.3 编译的都与入库产物零差异。所以这条红只证明工具链口味，不证明契约。用 pin 那把复跑：`GOTOOLCHAIN=go%s go test -run TestGeneratedMatchesProto .`；要换口味，就把 goversion 与用新插件重生成的 gen/go 一起提交（口径见风险登记册 §7.9）",
			gotGo, pinGo, pinGo)
	}

	// 把刚编出的插件所在目录前置到 PATH，让 buf 的 `local: protoc-gen-go` 命中它，
	// 而不是 PATH 上恰好装着的那把。
	genPath := filepath.Dir(plugin)
	if p := os.Getenv("PATH"); p != "" {
		genPath += string(os.PathListSeparator) + p
	}
	cmd := exec.Command("buf", "generate", "--template", "buf.gen.local.yaml", "-o", out)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+genPath)
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
		// 到这里三处都已对上（buf 版本 = pin、go.mod 编出的插件版本 = 产物头部声明、
		// 编译它的 Go major.minor = pin），所以这里的红才有「契约」含义。留一条退路：pin 是 major.minor
		// 粒度，同一 minor 的补丁位实测无关（go1.27.1 与 go1.27.3 同为零差异），但
		// **更旧的口味也可能换个补丁位复现**——所以文案仍要求复跑一次，只是复跑的对象
		// 换成「pin 那套工具链」而不是「另一个版本的同一把工具」。
		t.Fatalf("契约层生成物与 proto 不一致（%d 项，前 10 项）：\n  %s\n本机 buf=%s（= bconf/buf.version 的 pin），自编译的 protoc-gen-go=v%s（= bconf/go.mod 的 protobuf，且等于入库产物头部声明），编译它的 Go=go%s（= bconf/protoc-gen-go.goversion 的 pin）。\n三处都对上还差字节，就是契约真的变了：用 `buf generate --template buf.gen.local.yaml` 重生成并把 gen/go 与 proto 改动一起提交；若怀疑是本机工具链的补丁位口味，先按 §7.9 的矩阵换一把 pin 组合的插件复跑再判",
			len(problems), joinLimited(problems, 10), pinned, pluginWant, goVersionOfBinary(t, plugin))
	}
}

// selfBuiltPlugin 在 bconf module 里现编这条腿唯一可信的那把 protoc-gen-go，返回绝对路径。
// 版本轴因此由 bconf/go.mod 的 require 决定，口味轴因此由 `go build` 实际解析到的工具链
// 决定（GOTOOLCHAIN 换出的那把也如实进判据）。包源就在模块缓存里——bconf 的生成物本来就
// import protobuf，所以离线也能编。
func selfBuiltPlugin(t *testing.T, moduleDir string) string {
	t.Helper()
	name := "protoc-gen-go"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	plugin := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", plugin, "google.golang.org/protobuf/cmd/protoc-gen-go")
	cmd.Dir = moduleDir
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("自编译 protoc-gen-go 失败（在 %s 里 `go build google.golang.org/protobuf/cmd/protoc-gen-go`）: %v\n%s",
			moduleDir, err, b)
	}
	return plugin
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s: %v", path, err)
	}
	return b
}

// goVersionOfBinary 报出「编译这把插件用的 Go」的 major.minor。
// 这一轴在插件自己的 `--version` 里看不见（那只报 protobuf 的 module 版本），
// 而产物字节恰恰跟着它走，所以从二进制的构建信息里读：`go version -m <插件>` 首行
// 形如 `/path/protoc-gen-go: go1.27.1`。补丁位实测无关（go1.27.1 与 go1.27.3 编译的
// 同一版插件都与入库产物零差异），故只取到 major.minor。
func goVersionOfBinary(t *testing.T, path string) string {
	t.Helper()
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
