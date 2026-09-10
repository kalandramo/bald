package stringcase

import (
	"reflect"
	"testing"
)

func TestToSnakeCase(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"snake_case", "snake_case"},
		{"CamelCase", "camel_case"},
		{"lowerCamelCase", "lower_camel_case"},
		{"F", "f"},
		{"Foo", "foo"},
		{"FooB", "foo_b"},
		{"FooID", "foo_id"},
		{" FooBar\t", "foo_bar"},
		{"HTTPStatusCode", "http_status_code"},
		{"ParseURL.DoParse", "parse_url_do_parse"},
		{"Convert Space", "convert_space"},
		{"Convert-dash", "convert_dash"},
		{"Skip___MultipleUnderscores", "skip_multiple_underscores"},
		{"Skip   MultipleSpaces", "skip_multiple_spaces"},
		{"Skip---MultipleDashes", "skip_multiple_dashes"},
		{"Hello World", "hello_world"},
		{"Multiple Words Example", "multiple_words_example"},
		{"", ""},
		{"A", "a"},
		{"z", "z"},
		{"Special-Characters_Test", "special_characters_test"},
		{"Numbers123Test", "numbers123_test"},
		{"Hello World!", "hello_world"},
		{"Test@With#Symbols", "test_with_symbols"},
		{"ComplexCase123!@#", "complex_case123"},
		{"md5_hash", "md5_hash"},
		{"md5Hash", "md5_hash"},
		{"md5SHA", "md5_sha"},
		{"userID", "user_id"},
	}
	for _, tt := range tests {
		if got := ToSnakeCase(tt.input); got != tt.expected {
			t.Errorf("ToSnakeCase(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestUpperSnakeCase(t *testing.T) {
	if got := UpperSnakeCase("CamelCase"); got != "CAMEL_CASE" {
		t.Errorf("UpperSnakeCase(CamelCase) = %q, want CAMEL_CASE", got)
	}
}

func TestIsSnakeCase(t *testing.T) {
	valid := []string{"snake_case", "a", "a1", "foo_bar_baz", "md5_hash"}
	for _, s := range valid {
		if !IsSnakeCase(s) {
			t.Errorf("IsSnakeCase(%q) should be true", s)
		}
	}
	invalid := []string{"", "_leading", "trailing_", "Double__Underscore", "hasCapital", "has-dash", "has space"}
	for _, s := range invalid {
		if IsSnakeCase(s) {
			t.Errorf("IsSnakeCase(%q) should be false", s)
		}
	}
}

func TestSplit(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"CamelCase", []string{"Camel", "Case"}},
		{"lowerCamelCase", []string{"lower", "Camel", "Case"}},
		{"HTTPStatusCode", []string{"HTTP", "Status", "Code"}},
		// 字母/数字在分词层分离（字母+数字合并发生在 ToSnakeCase 的 delimiterCase 层）
		{"md5Hash", []string{"md", "5", "Hash"}},
		{"", []string{""}},
	}
	for _, tt := range tests {
		if got := Split(tt.input); !reflect.DeepEqual(got, tt.expected) {
			t.Errorf("Split(%q) = %v, want %v", tt.input, got, tt.expected)
		}
	}
}
