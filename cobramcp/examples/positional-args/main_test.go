package main

// This test file verifies that each command's positional arguments and flags are
// correctly reflected in the generated MCP Tool JSON Schema.
//
// The generated schema is FLAT: every flag and every positional argument appears
// as a direct top-level property — there is no nested "flags" or "args" object.
//
// For each pattern we check:
//   - Each argument token maps to a correctly-typed top-level property.
//   - Required tokens appear in the top-level "required" list; optional ones do not.
//   - Variadic tokens map to an array-of-string property.
//   - Flags appear next to the positional arguments at the top level.
//   - Annotation-supplied descriptions are preserved verbatim.

import (
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/kalandramo/bald/cobramcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────────────────────────────────────
// Pattern 1 – oncall <module> [title]
// ─────────────────────────────────────────────────────────────────────────────

func TestOncallSchema(t *testing.T) {
	srv, err := cobramcp.NewMCPServer(cobramcp.MCPOptions{Enabled: false, Name: "myops"}, buildRootCmd)
	require.NoError(t, err)

	schema := findToolInputSchema(t, srv, "oncall")

	// 扁平 schema：不出现 "args" / "flags" / "positional_args" 这类包装对象。
	assert.NotContains(t, schema.Properties, "args", "oncall: args must be flat")
	assert.NotContains(t, schema.Properties, "flags", "oncall: flags must be flat")
	assert.NotContains(t, schema.Properties, "positional_args",
		"oncall: positional_args key must not exist")

	// <module> — required, string
	require.Contains(t, schema.Properties, "module")
	assert.Equal(t, "string", schema.Properties["module"].Type)
	assert.Contains(t, schema.Required, "module", "<module> is required")

	// [title] — optional, string
	require.Contains(t, schema.Properties, "title")
	assert.Equal(t, "string", schema.Properties["title"].Type)
	assert.NotContains(t, schema.Required, "title", "[title] is optional")

	// 注解带来的描述必须原样保留。
	assert.Contains(t, schema.Properties["module"].Description, "bke",
		"module description should mention example values from annotation")
	assert.Contains(t, schema.Properties["title"].Description, "可选",
		"title description should mention it is optional")
}

// ─────────────────────────────────────────────────────────────────────────────
// Pattern 2 – cp <src> <dst>
// ─────────────────────────────────────────────────────────────────────────────

func TestCpSchema(t *testing.T) {
	srv, err := cobramcp.NewMCPServer(cobramcp.MCPOptions{Enabled: false, Name: "myops"}, buildRootCmd)
	require.NoError(t, err)

	schema := findToolInputSchema(t, srv, "cp")

	// 两个位置参数都必须是顶层必填字符串。
	for _, name := range []string{"src", "dst"} {
		require.Contains(t, schema.Properties, name, "cp: property %q missing", name)
		assert.Equal(t, "string", schema.Properties[name].Type)
		assert.Contains(t, schema.Required, name, "cp: %q must be required", name)
	}

	// 注解描述。
	assert.Contains(t, schema.Properties["src"].Description, "源文件")
	assert.Contains(t, schema.Properties["dst"].Description, "目标")
}

// ─────────────────────────────────────────────────────────────────────────────
// Pattern 3 – make [targets...]
// ─────────────────────────────────────────────────────────────────────────────

func TestMakeSchema(t *testing.T) {
	srv, err := cobramcp.NewMCPServer(cobramcp.MCPOptions{Enabled: false, Name: "myops"}, buildRootCmd)
	require.NoError(t, err)

	schema := findToolInputSchema(t, srv, "make")

	// 变长位置参数 → 顶层 array 属性。
	require.Contains(t, schema.Properties, "targets")
	targetsSchema := schema.Properties["targets"]
	assert.Equal(t, "array", targetsSchema.Type,
		"make: [targets...] should map to array type")
	require.NotNil(t, targetsSchema.Items)
	assert.Equal(t, "string", targetsSchema.Items.Type)

	// [targets...] 可选 → 不出现在 required。
	assert.NotContains(t, schema.Required, "targets",
		"make: [targets...] is optional, must not be required")

	// 注解描述必须保留。
	assert.Contains(t, targetsSchema.Description, "Makefile")
}

// ─────────────────────────────────────────────────────────────────────────────
// Pattern 4 – kubectl sub-command tree
// ─────────────────────────────────────────────────────────────────────────────

func TestKubectlGetSchema(t *testing.T) {
	srv, err := cobramcp.NewMCPServer(cobramcp.MCPOptions{Enabled: false, Name: "myops"}, buildRootCmd)
	require.NoError(t, err)

	// Tool name for "kubectl get" with empty prefix → "kubectl_get"
	schema := findToolInputSchema(t, srv, "kubectl_get")

	// <resource> required, [name] optional
	require.Contains(t, schema.Properties, "resource")
	assert.Equal(t, "string", schema.Properties["resource"].Type)
	assert.Contains(t, schema.Required, "resource")

	require.Contains(t, schema.Properties, "name")
	assert.Equal(t, "string", schema.Properties["name"].Type)
	assert.NotContains(t, schema.Required, "name")

	// 标志与位置参数同为顶层属性。
	require.Contains(t, schema.Properties, "namespace")
	require.Contains(t, schema.Properties, "output")
	assert.Equal(t, "string", schema.Properties["namespace"].Type)
}

func TestKubectlLogsSchema(t *testing.T) {
	srv, err := cobramcp.NewMCPServer(cobramcp.MCPOptions{Enabled: false, Name: "myops"}, buildRootCmd)
	require.NoError(t, err)

	// "logs <pod> [flags]" — [flags] 是文档占位符，不产生任何属性。
	schema := findToolInputSchema(t, srv, "kubectl_logs")

	require.Contains(t, schema.Properties, "pod")
	assert.Equal(t, "string", schema.Properties["pod"].Type)
	assert.Contains(t, schema.Required, "pod")

	// [flags] 哨兵不得变成属性。
	assert.NotContains(t, schema.Properties, "flags",
		"[flags] sentinel must be ignored by cobramcp")
}

func TestKubectlExecSchema(t *testing.T) {
	srv, err := cobramcp.NewMCPServer(cobramcp.MCPOptions{Enabled: false, Name: "myops"}, buildRootCmd)
	require.NoError(t, err)

	// "exec <pod> <cmd...>" — pod required string, cmd required string array
	schema := findToolInputSchema(t, srv, "kubectl_exec")

	require.Contains(t, schema.Properties, "pod")
	assert.Equal(t, "string", schema.Properties["pod"].Type)
	assert.Contains(t, schema.Required, "pod")

	require.Contains(t, schema.Properties, "cmd")
	cmdSchema := schema.Properties["cmd"]
	assert.Equal(t, "array", cmdSchema.Type,
		"exec: <cmd...> should be a string array")
	require.NotNil(t, cmdSchema.Items)
	assert.Equal(t, "string", cmdSchema.Items.Type)
	assert.Contains(t, schema.Required, "cmd",
		"exec: <cmd...> is required")
}

// ─────────────────────────────────────────────────────────────────────────────
// Tool inventory
// ─────────────────────────────────────────────────────────────────────────────

func TestToolInventory(t *testing.T) {
	srv, err := cobramcp.NewMCPServer(cobramcp.MCPOptions{Enabled: false, Name: "myops"}, buildRootCmd)
	require.NoError(t, err)

	tools := srv.Tools()
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}

	expected := []string{
		"oncall",
		"cp",
		"make",
		"kubectl_get",
		"kubectl_logs",
		"kubectl_exec",
	}
	for _, e := range expected {
		assert.Contains(t, names, e, "expected tool %q to be registered", e)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// helper: find a tool's input schema by its name suffix
// ─────────────────────────────────────────────────────────────────────────────

func findToolInputSchema(t *testing.T, srv *cobramcp.MCPServer, toolName string) *jsonschema.Schema {
	t.Helper()
	for _, tool := range srv.Tools() {
		if tool.Name == toolName {
			require.NotEmpty(t, tool.RawInputSchema, "tool %q: RawInputSchema must not be empty", toolName)
			s := &jsonschema.Schema{}
			require.NoError(t, json.Unmarshal(tool.RawInputSchema, s))
			return s
		}
	}
	names := make([]string, 0)
	for _, tool := range srv.Tools() {
		names = append(names, tool.Name)
	}
	t.Fatalf("tool %q not found; registered tools: %v", toolName, names)
	return nil
}
