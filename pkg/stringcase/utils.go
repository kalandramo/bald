package stringcase

import (
	"strings"
	"unicode"
)

// SplitByNonAlphanumeric 把非英文字符和数字的字符替换为空格后按空白分割。
func SplitByNonAlphanumeric(input string) []string {
	var builder strings.Builder
	for _, r := range input {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
		} else {
			builder.WriteRune(' ')
		}
	}
	return strings.Fields(builder.String())
}

// ContainsFn 在 slice 中按自定义谓词查找 value。
func ContainsFn[T any](slice []T, value T, predicate func(got, want T) bool) bool {
	for _, item := range slice {
		if predicate(item, value) {
			return true
		}
	}
	return false
}
