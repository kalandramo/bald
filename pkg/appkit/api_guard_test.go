// api_guard_test.go —— 守卫：本仓生产代码不出现未类型化资源访问。
//
// 守卫范围：仓库根下所有非测试 .go 文件，AST 扫描 `X.Resource("字面量")`
// 形态的调用。类型化句柄 `Key[T].Resource(app)` 的唯一实参是 *App，不可能
// 是字符串字面量，因此不会误报。App.Resource(string) 已删除，
// 守卫同时以反射钉住「公开方法不存在」——字符串入口无法再悄悄回来。
//
// bald-admin 是仓外消费方，其迁移不在本守卫范围。
package appkit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestAPI_NoUntypedResource(t *testing.T) {
	root := repoRoot(t)

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

	// 字符串入口已删除——公开 API 面不得再长出 Resource(string)。
	if m, ok := reflect.TypeOf(&App{}).MethodByName("Resource"); ok {
		t.Fatalf("App.Resource must stay deleted (found method %s); typed keys are the only resource access", m.Type)
	}
}

// TestAPI_NoManualOrderingTables 钉住 §7.1「手工排序表 = 0」：生产代码不得
// 再出现 stageBRank / cleanupOrder 标识符。注释里的历史说明不触发——AST 只
// 看标识符，不看注释。排序必须是节点数据（Phase/Priority/Key/dependsOn）
// 的函数，否则新增一个节点就要有人记得改表。
func TestAPI_NoManualOrderingTables(t *testing.T) {
	forbidden := map[string]bool{"stageBRank": true, "cleanupOrder": true}
	var violations []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(repoRoot(t), func(path string, d os.DirEntry, err error) error {
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
		f, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && forbidden[id.Name] {
				violations = append(violations, fset.Position(id.Pos()).String()+" ("+id.Name+")")
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("manual ordering tables found (order must derive from node data):\n%s",
			strings.Join(violations, "\n"))
	}
}

// repoRoot 返回仓库根（本文件位于 pkg/appkit）。
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file))) // pkg/appkit → repo root
}
