package cobramcp

import (
	"strconv"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/spf13/cobra"

	baldlog "github.com/kalandramo/bald/log"
)

// Cobra command annotation keys for MCP tool annotations.
// Set these in cmd.Annotations to populate mcp.ToolAnnotation on the generated tool.
//
// Boolean values are parsed with strconv.ParseBool (accepts "1", "t", "true", "0", "f", "false", etc.).
//
// Example:
//
//	cmd.Annotations = map[string]string{
//	    cobramcp.AnnotationReadOnly: "true",
//	    cobramcp.AnnotationTitle:    "List files",
//	}
const (
	// AnnotationTitle sets the human-readable title for the tool.
	AnnotationTitle = "title"

	// AnnotationReadOnly hints that the tool does not modify its environment.
	AnnotationReadOnly = "readOnlyHint"

	// AnnotationDestructive hints that the tool may perform destructive updates.
	// Only meaningful when ReadOnlyHint is false.
	AnnotationDestructive = "destructiveHint"

	// AnnotationIdempotent hints that calling the tool repeatedly with the same
	// arguments has no additional effect. Only meaningful when ReadOnlyHint is false.
	AnnotationIdempotent = "idempotentHint"

	// AnnotationOpenWorld hints that the tool may interact with external entities
	// outside its closed domain.
	AnnotationOpenWorld = "openWorldHint"

	// AnnotationOutputJSON 声明该命令的 stdout 是一份 JSON 契约（对象或数组）。
	//
	// 它不进入 mcp.ToolAnnotation（MCP 协议没有对应字段），只在注册期被
	// cobramcp 自己读走：启用的命令，其调用结果的 structuredContent 会多出
	// 一个 data 键，承载解析后的 JSON 原值，让 LLM 能直接据此组装后续调用，
	// 而不是重新解析一段文本。文本轨道完全不受影响——解析失败也一样。
	//
	// 布尔值语义与其它布尔注解一致（strconv.ParseBool："1"/"true"/"0"/"false"）。
	AnnotationOutputJSON = "outputJSONHint"
)

// boolAnnotation parses a boolean annotation.
//
// ok is false when the key is absent or its value is not a bool; in the latter
// case a warning is logged and the annotation is skipped rather than failing
// registration.
func boolAnnotation(cmd *cobra.Command, key string) (value, ok bool) {
	raw, exists := cmd.Annotations[key]
	if !exists {
		return false, false
	}

	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		baldlog.Warn(nil, "invalid bool value for annotation, skipping", "key", key, "value", raw)

		return false, false
	}

	return parsed, true
}

// outputJSONHint reports whether cmd declares its stdout to be a JSON contract
// (see [AnnotationOutputJSON]). Absent or unparsable values mean "no hint".
func outputJSONHint(cmd *cobra.Command) bool {
	enabled, ok := boolAnnotation(cmd, AnnotationOutputJSON)

	return ok && enabled
}

// toolAnnotations reads MCP annotation keys from cmd.Annotations and
// returns a populated *mcp.ToolAnnotation, or nil if no MCP annotations are found.
func toolAnnotations(cmd *cobra.Command) *mcp.ToolAnnotation {
	if len(cmd.Annotations) == 0 {
		return nil
	}

	var annotations mcp.ToolAnnotation
	found := false

	if v, ok := cmd.Annotations[AnnotationTitle]; ok {
		annotations.Title = v
		found = true
	}

	// 布尔注解：键名 → 目标字段的写入口，解析失败只告警不中断。
	boolAnnotations := []struct {
		key   string
		apply func(*bool)
	}{
		{AnnotationReadOnly, func(b *bool) { annotations.ReadOnlyHint = b }},
		{AnnotationDestructive, func(b *bool) { annotations.DestructiveHint = b }},
		{AnnotationIdempotent, func(b *bool) { annotations.IdempotentHint = b }},
		{AnnotationOpenWorld, func(b *bool) { annotations.OpenWorldHint = b }},
	}

	for _, item := range boolAnnotations {
		if parsed, ok := boolAnnotation(cmd, item.key); ok {
			item.apply(&parsed)
			found = true
		}
	}

	if !found {
		return nil
	}

	return &annotations
}
