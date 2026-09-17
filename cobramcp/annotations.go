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
)

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
		raw, ok := cmd.Annotations[item.key]
		if !ok {
			continue
		}

		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			baldlog.Warn(nil, "invalid bool value for annotation, skipping", "key", item.key, "value", raw)
			continue
		}

		item.apply(&parsed)
		found = true
	}

	if !found {
		return nil
	}

	return &annotations
}
