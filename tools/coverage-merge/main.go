// coverage-merge 替代 scripts/coverage.sh 内嵌的 awk 合并器（ADR-7）：
// 多腿 coverprofile 并集成逐包报表。语义与 awk 版逐条对应——
//
//   - 同一 block（file:行域）出现在多腿时取「命中次数的最大值」（任一腿命中即
//     覆盖），不做语句累加（naive sum 会让两腿共有的包分母翻倍）；
//   - 排除生成物与入口：*.pb.go / *.pb.gw.go / *_grpc.pb.go / *.pb.validate.go /
//     *.gen.go / main.go / /mocks?/ 目录；
//   - 行内豁免：block 行域内出现 `//cov:excl` 即从分子与分母同时剔除；
//   - 逐包统计（不做混合总数），末行输出手写代码总聚合；--gate N 时任一
//     纳入包低于 N% 退出码 1。
//
// 与 awk 版的差异（有意为之）：输出按包名排序（awk 是哈希序，两次运行不稳定）；
// 其余字节一致。用法：coverage-merge [--gate N] <dir|file>…（dir 下取 *.out）。
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type block struct {
	stmts int
	count int
}

func main() {
	gate := 0.0
	haveGate := false
	var paths []string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--gate":
			if i+1 >= len(args) {
				fatal("--gate 需要百分比数字")
			}
			v, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil {
				fatal("--gate 需要百分比数字: %v", err)
			}
			gate, haveGate = v, true
			i++
		default:
			paths = append(paths, args[i])
		}
	}
	if len(paths) == 0 {
		fatal("用法: coverage-merge [--gate N] <dir|file>…")
	}

	var profiles []string
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			fatal("stat %s: %v", p, err)
		}
		if st.IsDir() {
			matches, err := filepath.Glob(filepath.Join(p, "*.out"))
			if err != nil {
				fatal("glob %s: %v", p, err)
			}
			if len(matches) == 0 {
				fatal("coverage: %s 下无 *.out profile，拒绝空判", p)
			}
			profiles = append(profiles, matches...)
		} else {
			profiles = append(profiles, p)
		}
	}

	blocks := map[string]*block{}
	for _, prof := range profiles {
		f, err := os.Open(prof)
		if err != nil {
			fatal("open %s: %v", prof, err)
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "mode:") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 3 {
				continue
			}
			cnt, err := strconv.Atoi(fields[2])
			if err != nil {
				continue
			}
			numstmt, err := strconv.Atoi(fields[1])
			if err != nil {
				continue
			}
			loc := fields[0]
			b, ok := blocks[loc]
			if !ok {
				blocks[loc] = &block{stmts: numstmt, count: cnt}
				continue
			}
			if cnt > b.count { // 并集语义：任一腿命中即覆盖
				b.count = cnt
			}
		}
		_ = f.Close()
	}
	if len(blocks) == 0 {
		fatal("coverage: profile 为空，拒绝空判")
	}

	exclCache := map[string][]string{}
	isExcl := func(file string, sl, el int) bool {
		rel := strings.TrimPrefix(file, "github.com/kalandramo/bald/")
		lines, ok := exclCache[rel]
		if !ok {
			data, err := os.ReadFile(rel)
			if err != nil {
				exclCache[rel] = nil
				return false
			}
			lines = strings.Split(string(data), "\n")
			exclCache[rel] = lines
		}
		for i := sl; i <= el && i <= len(lines); i++ {
			if i >= 1 && strings.Contains(lines[i-1], "cov:excl") {
				return true
			}
		}
		return false
	}

	total, covered := 0, 0
	type row struct{ pkg string; stmts, cov int }
	perPkg := map[string]*row{}
	for loc, b := range blocks {
		file := loc
		if i := strings.Index(loc, ":"); i >= 0 {
			file = loc[:i]
		}
		base := filepath.Base(file)
		if strings.HasSuffix(base, ".pb.go") || strings.HasSuffix(base, ".pb.gw.go") ||
			strings.HasSuffix(base, "_grpc.pb.go") || strings.HasSuffix(base, ".pb.validate.go") ||
			strings.HasSuffix(base, ".gen.go") || base == "main.go" ||
			strings.Contains(file, "/mocks/") || strings.Contains(file, "/mock/") {
			continue
		}
		rng := loc[strings.Index(loc, ":")+1:]
		parts := strings.Split(rng, ",")
		if len(parts) != 2 {
			continue
		}
		sl, err1 := strconv.Atoi(strings.SplitN(parts[0], ".", 2)[0])
		el, err2 := strconv.Atoi(strings.SplitN(parts[1], ".", 2)[0])
		if err1 != nil || err2 != nil {
			continue
		}
		if isExcl(file, sl, el) {
			continue
		}
		pkg := file
		if i := strings.LastIndex(file, "/"); i >= 0 {
			pkg = file[:i]
		}
		r := perPkg[pkg]
		if r == nil {
			r = &row{pkg: pkg}
			perPkg[pkg] = r
		}
		r.stmts += b.stmts
		if b.count > 0 {
			r.cov += b.stmts
		}
		total += b.stmts
		if b.count > 0 {
			covered += b.stmts
		}
	}

	fmt.Printf("%-64s %8s %8s %7s\n", "package", "stmts", "cov", "%")
	keys := make([]string, 0, len(perPkg))
	for k := range perPkg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	below := 0
	for _, k := range keys {
		r := perPkg[k]
		pc := 100.0
		if r.stmts > 0 {
			pc = float64(r.cov) * 100 / float64(r.stmts)
		}
		fmt.Printf("%-64s %8d %8d %6.1f%%\n", k, r.stmts, r.cov, pc)
		if haveGate && pc < gate {
			below++
			fmt.Printf("BELOW %v%%: %s (%.1f%%)\n", gate, k, pc)
		}
	}
	fmt.Printf("\nAGGREGATE(hand-written, excl. generated/main): %d/%d = %.2f%%\n", covered, total, pct(covered, total))
	if haveGate {
		if below > 0 {
			fmt.Printf("\nGATE FAIL: %d packages below %v%%\n", below, gate)
			os.Exit(1)
		}
		fmt.Printf("\nGATE PASS: all packages >= %v%%\n", gate)
	}
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) * 100 / float64(b)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "coverage-merge: "+format+"\n", args...)
	os.Exit(1)
}
