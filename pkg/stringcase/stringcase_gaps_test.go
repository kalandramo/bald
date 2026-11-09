package stringcase

import (
	"reflect"
	"testing"
)

// TestSnakeCase_AllDelimiters 非空但全为分隔符的输入，分词后无有效词 → 返回空串。
func TestSnakeCase_AllDelimiters(t *testing.T) {
	for _, in := range []string{"___", "!@#", "---", "   "} {
		if got := ToSnakeCase(in); got != "" {
			t.Errorf("ToSnakeCase(%q) = %q, want empty", in, got)
		}
	}
}

// TestIsSnakeCase_ConsecutiveUnderscore 小写串中的连续下划线必须判否
// （大写串会先因非法字符退出，触达不了此分支）。
func TestIsSnakeCase_ConsecutiveUnderscore(t *testing.T) {
	if IsSnakeCase("a__b") {
		t.Error("IsSnakeCase(a__b) should be false (consecutive underscores)")
	}
	if !IsSnakeCase("a_b") {
		t.Error("IsSnakeCase(a_b) should be true")
	}
}

// TestSplit_NoSplit noSplit 词应保持不拆分（触达 isNoSplitWord 的 HasPrefix 谓词）。
func TestSplit_NoSplit(t *testing.T) {
	if got := Split("123abc", "123abc"); !reflect.DeepEqual(got, []string{"123abc"}) {
		t.Errorf("Split(123abc, noSplit=123abc) = %v, want [123abc]", got)
	}
	// 无 noSplit 时数字与字母分离。
	if got := Split("123abc"); !reflect.DeepEqual(got, []string{"123", "abc"}) {
		t.Errorf("Split(123abc) = %v, want [123 abc]", got)
	}
}

// TestSplitLowercase_Guard 私有 split 的空串/非法 UTF-8 守卫：原样返回单元素。
func TestSplitLowercase_Guard(t *testing.T) {
	if got := split(""); !reflect.DeepEqual(got, []string{""}) {
		t.Errorf("split(empty) = %v, want [\"\"]", got)
	}
	if got := split("\xff"); !reflect.DeepEqual(got, []string{"\xff"}) {
		t.Errorf("split(invalid utf8) = %v, want [\\xff]", got)
	}
}

// TestContainsFn 谓词命中返回 true，未命中返回 false。
func TestContainsFn(t *testing.T) {
	eq := func(got, want string) bool { return got == want }
	if !ContainsFn([]string{"a", "b", "c"}, "b", eq) {
		t.Error("ContainsFn should find b")
	}
	if ContainsFn([]string{"a", "b"}, "z", eq) {
		t.Error("ContainsFn should not find z")
	}
	if ContainsFn([]string(nil), "a", eq) {
		t.Error("ContainsFn on empty slice should be false")
	}
}
