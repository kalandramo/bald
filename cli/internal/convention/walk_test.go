package convention

import (
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// TestExemptCountTowardScanned 钉住豁免与 scanned 计数的先后顺序：豁免文件
// 计入 skip 判据的 scanned（旧 walkAST / 旧 logging 的口径）——全豁免的树
// 是 ran 不是 skip，--min-coverage 门依赖该口径。首版实现曾把豁免过滤提前
// 到计数之前，全豁免模块 ran→skip、覆盖率掉档（审阅双二进制实证后修正）。
func TestExemptCountTowardScanned(t *testing.T) {
	t.Run("logging-stdlib：全豁免 → ran", func(t *testing.T) {
		dir := writeTree(t, map[string]string{
			"go.mod":   "module demo\n",
			"log/x.go": "package log\n",
		})
		findings, skip, err := checkLoggingStdlib(dir)
		if err != nil || skip != "" || len(findings) != 0 {
			t.Fatalf("findings=%v skip=%q err=%v, want ran with 0 findings", findings, skip, err)
		}
	})
	t.Run("checkAST：豁免计入 scanned → ran", func(t *testing.T) {
		dir := writeTree(t, map[string]string{
			"go.mod":   "module demo\n",
			"log/x.go": "package log\n",
		})
		findings, skip, err := checkAST(dir,
			astWalk{exempt: []string{"log/"}},
			astRule{ruleID: "exempt-scan-probe"},
			func(*goSource, func(token.Pos, string)) {})
		if err != nil || skip != "" || len(findings) != 0 {
			t.Fatalf("findings=%v skip=%q err=%v, want ran with 0 findings", findings, skip, err)
		}
	})
}

// BenchmarkRunCheck 度量一次完整 check 的墙钟（对仓库根自身）。dir 以包目录
// cli/internal/convention 为基：../../.. 是多 module 仓根，全部 module 一起
// 度量；只检出 cli module 的环境没有上级仓库根，跳过。缓存设计使每次 runCheck
// 恰好一次全树 walk+parse；对比旧实现（每 AST checker 一次独立遍历）用同一
// 基准在旧代码上跑同数即可。
func BenchmarkRunCheck(b *testing.B) {
	dir := "../../.."
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		b.Skip("no repo root (go.mod missing) at", dir)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := runCheck(checkOptions{Dir: dir, MinLevel: "mandatory"}); err != nil {
			b.Fatal(err)
		}
	}
}
