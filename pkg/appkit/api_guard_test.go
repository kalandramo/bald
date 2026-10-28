// api_guard_test.go —— I9：本仓生产代码不出现未类型化资源访问。
//
// 守卫范围：仓库根下所有非测试 .go 文件，AST 扫描 `X.Resource("字面量")`
// 形态的调用（deprecated 的 App.Resource(string)）。类型化句柄
// `Key[T].Resource(app)` 的唯一实参是 *App，不可能是字符串字面量，因此
// 不会误报；测试文件与 resource.go（deprecated 定义处）除外。
//
// bald-admin 是仓外消费方，其迁移由 M2 的独立验收覆盖，不在本守卫范围。
package appkit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAPI_NoUntypedResource(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file))) // pkg/appkit → repo root

	var violations []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if rel == filepath.Join("pkg", "appkit", "resource.go") {
			return nil // deprecated 方法定义处
		}
		f, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Resource" || len(call.Args) != 1 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if ok && lit.Kind == token.STRING {
				violations = append(violations, fset.Position(call.Pos()).String())
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("untyped Resource(string) calls found (use provider-exported appkit.Key[T] instead):\n%s",
			strings.Join(violations, "\n"))
	}
}
