package bconf

import (
	"reflect"
	"sync"
	"testing"
)

// deepCloneMap 深拷贝配置树（map[string]any / []any / 标量），供只读性断言。
func deepCloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = deepCloneValue(v)
	}
	return out
}

func deepCloneValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return deepCloneMap(t)
	case []any:
		out := make([]any, len(t))
		for i, it := range t {
			out[i] = deepCloneValue(it)
		}
		return out
	default:
		return v
	}
}

// TestFindUnknownKeys_Semantics 钉住 FindUnknownKeys 的可见行为：未知键
// 集合与路径形态（含列表下标、保留原始键大小写、顶层前导点）、返回顺序
// 确定（字节序排序，因 map 迭代序随机，重复调用必须产出同一序列）、白名单
// 段整树跳过且大小写归一、输入树只读、缓存冷/热与并发调用结果一致。
// 描述符级查找表缓存 + 描述符直传 + 路径串惰性拼接等优化的
// 语义不变性回归锚点。
func TestFindUnknownKeys_Semantics(t *testing.T) {
	tree := map[string]any{
		"APP": map[string]any{ // 顶层键大小写不敏感，命中契约段
			"id": "u",
		},
		"server": map[string]any{
			"http": map[string]any{"addr": ":8080", "unknownFlag": "x"},
		},
		"logger": map[string]any{
			"backends": []any{
				map[string]any{"type": "slog", "slog": map[string]any{"level": "info", "ghost": 1}},
				map[string]any{"type": "loki", "loki": map[string]any{"endpoint": "http://x"}},
			},
		},
		"telemetry": map[string]any{"enabled": true},
		"business":  map[string]any{"fallbackTenant": map[string]any{"deep": true}},
	}
	before := deepCloneMap(tree)

	want := []string{
		".business",
		".logger.backends[0].slog.ghost",
		".server.http.unknownFlag",
		".telemetry",
	}

	if got := FindUnknownKeys(tree, NewBootstrap()); !reflect.DeepEqual(got, want) {
		t.Fatalf("unknown keys = %q, want %q", got, want)
	}

	// 白名单段整树跳过（business 为业务自持段）。
	wantAllowed := want[1:]
	if got := FindUnknownKeys(tree, NewBootstrap(), "business"); !reflect.DeepEqual(got, wantAllowed) {
		t.Fatalf("unknown keys with allowTop = %q, want %q", got, wantAllowed)
	}
	// 白名单元素大小写归一（allowTop 会被 ToLower 后比对）。
	if got := FindUnknownKeys(tree, NewBootstrap(), "BUSINESS"); !reflect.DeepEqual(got, wantAllowed) {
		t.Fatalf("unknown keys with uppercase allowTop = %q, want %q", got, wantAllowed)
	}

	// 输入树只读。
	if !reflect.DeepEqual(tree, before) {
		t.Fatalf("input tree mutated: %v", tree)
	}

	// 重复调用（查找表缓存热）结果一致——钉住顺序确定性。
	for i := 0; i < 20; i++ {
		if got := FindUnknownKeys(tree, NewBootstrap()); !reflect.DeepEqual(got, want) {
			t.Fatalf("warm call %d: unknown keys = %q, want %q", i, got, want)
		}
	}

	// 并发调用（sync.Map 缓存共读 + 只读查找表）结果一致。
	var wg sync.WaitGroup
	results := make([][]string, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = FindUnknownKeys(tree, NewBootstrap(), "business")
		}(i)
	}
	wg.Wait()
	for i, r := range results {
		if !reflect.DeepEqual(r, wantAllowed) {
			t.Fatalf("concurrent call %d = %q, want %q", i, r, wantAllowed)
		}
	}

	// 全已知树：无未知键（空返回）。
	known := map[string]any{"app": map[string]any{"id": "x", "name": "y"}}
	if got := FindUnknownKeys(known, NewBootstrap()); len(got) != 0 {
		t.Fatalf("known tree: unknown keys = %q, want none", got)
	}

	// nil 入参：返回 nil。
	if got := FindUnknownKeys(nil, NewBootstrap()); got != nil {
		t.Fatalf("nil tree: got %q, want nil", got)
	}
	if got := FindUnknownKeys(tree, nil); got != nil {
		t.Fatalf("nil msg: got %q, want nil", got)
	}
}
