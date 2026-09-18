package cobramcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildFlagArgs(t *testing.T) {
	tests := []struct {
		name     string
		flags    map[string]any
		expected []string
	}{
		{
			name:     "Empty flags",
			flags:    map[string]any{},
			expected: []string{},
		},
		{
			name: "Boolean flags",
			flags: map[string]any{
				"verbose": true,
				"quiet":   false,
				"debug":   true,
			},
			expected: []string{"--verbose", "--debug"},
		},
		{
			name: "String flags",
			flags: map[string]any{
				"output": "result.txt",
				"format": "json",
			},
			expected: []string{"--output", "result.txt", "--format", "json"},
		},
		{
			name: "Integer flags",
			flags: map[string]any{
				"count":   10,
				"timeout": 30,
			},
			expected: []string{"--count", "10", "--timeout", "30"},
		},
		{
			name: "Float flags",
			flags: map[string]any{
				"ratio":     0.75,
				"threshold": 1.5,
			},
			expected: []string{"--ratio", "0.75", "--threshold", "1.5"},
		},
		{
			name: "Array flags",
			flags: map[string]any{
				"include": []any{"*.go", "*.md"},
				"exclude": []any{"vendor"},
			},
			expected: []string{"--include", "*.go", "--include", "*.md", "--exclude", "vendor"},
		},
		{
			name: "Mixed types",
			flags: map[string]any{
				"verbose": true,
				"output":  "result.txt",
				"count":   5,
				"tags":    []any{"test", "debug"},
			},
			expected: []string{"--verbose", "--output", "result.txt", "--count", "5", "--tags", "test", "--tags", "debug"},
		},
		{
			name: "Map flags (stringToString)",
			flags: map[string]any{
				"labels": map[string]any{
					"env":  "prod",
					"team": "backend",
				},
			},
			expected: []string{"--labels", "env=prod", "--labels", "team=backend"},
		},
		{
			name: "Empty map",
			flags: map[string]any{
				"labels": map[string]any{},
			},
			expected: []string{},
		},
		{
			name: "StringToInt map flags",
			flags: map[string]any{
				"ports": map[string]any{
					"http":  8080,
					"https": 8443,
				},
			},
			expected: []string{"--ports", "http=8080", "--ports", "https=8443"},
		},
		{
			name: "StringToInt64 map flags",
			flags: map[string]any{
				"sizes": map[string]any{
					"small":  int64(1024),
					"medium": int64(2048),
					"large":  int64(4096),
				},
			},
			expected: []string{"--sizes", "small=1024", "--sizes", "medium=2048", "--sizes", "large=4096"},
		},
		{
			name: "Nil values",
			flags: map[string]any{
				"flag1": nil,
				"flag2": "value",
				"flag3": nil,
			},
			expected: []string{"--flag2", "value"},
		},
		{
			name: "Empty flag name",
			flags: map[string]any{
				"":      "value",
				"valid": "value",
			},
			expected: []string{"--valid", "value"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildFlagArgs(tt.flags)
			// Sort both slices for comparison since map iteration order is not guaranteed
			assert.ElementsMatch(t, tt.expected, result)
		})
	}
}

// makeFlatInput is a helper to build a ToolInput from a command path, flat map,
// flag set and ordered arg name list — mirrors what decodeToolInput produces at
// runtime (cmdPath recorded at registration time, everything else per call).
func makeFlatInput(cmdPath []string, flat map[string]any, flagNames []string, argNames []string) ToolInput {
	fn := make(map[string]struct{}, len(flagNames))
	for _, n := range flagNames {
		fn[n] = struct{}{}
	}
	return ToolInput{
		CmdPath:   cmdPath,
		FlatInput: flat,
		FlagNames: fn,
		ArgNames:  argNames,
	}
}

func TestBuildCommandArgs(t *testing.T) {
	tests := []struct {
		name         string
		cmdPath      []string
		input        ToolInput
		expectedArgs []string
	}{
		{
			name:         "Simple command",
			cmdPath:      []string{"test"},
			input:        makeFlatInput([]string{"test"}, map[string]any{}, nil, nil),
			expectedArgs: []string{"test"},
		},
		{
			name:         "Nested command",
			cmdPath:      []string{"sub", "command"},
			input:        makeFlatInput([]string{"sub", "command"}, map[string]any{}, nil, nil),
			expectedArgs: []string{"sub", "command"},
		},
		{
			name:    "Command with flags",
			cmdPath: []string{"test"},
			input: makeFlatInput(
				[]string{"test"},
				map[string]any{"verbose": true, "output": "result.txt"},
				[]string{"verbose", "output"},
				nil,
			),
			expectedArgs: []string{"test", "--verbose", "--output", "result.txt"},
		},
		{
			name:    "Command with arguments",
			cmdPath: []string{"test"},
			// ArgNames determines order: a_file1 then b_file2
			input: makeFlatInput(
				[]string{"test"},
				map[string]any{"a_file1": "file1.txt", "b_file2": "file2.txt"},
				nil,
				[]string{"a_file1", "b_file2"},
			),
			expectedArgs: []string{"test", "file1.txt", "file2.txt"},
		},
		{
			name:    "Command with flags and arguments",
			cmdPath: []string{"deploy"},
			input: makeFlatInput(
				[]string{"deploy"},
				map[string]any{
					"namespace": "production",
					"replicas":  3,
					"wait":      true,
					"app":       "my-app",
					"version":   "v1.2.3",
				},
				[]string{"namespace", "replicas", "wait"},
				[]string{"app", "version"},
			),
			expectedArgs: []string{"deploy", "--namespace", "production", "--replicas", "3", "--wait", "my-app", "v1.2.3"},
		},
		{
			name:    "Complex nested command",
			cmdPath: []string{"cluster", "node", "list"},
			input: makeFlatInput(
				[]string{"cluster", "node", "list"},
				map[string]any{
					"output": "json",
					"label":  []any{"env=prod", "team=backend"},
				},
				[]string{"output", "label"},
				nil,
			),
			expectedArgs: []string{"cluster", "node", "list", "--output", "json", "--label", "env=prod", "--label", "team=backend"},
		},
		{
			name:    "Command with map flags",
			cmdPath: []string{"deploy"},
			input: makeFlatInput(
				[]string{"deploy"},
				map[string]any{
					"labels": map[string]any{
						"env":     "production",
						"version": "v1.2.3",
					},
					"wait": true,
					"app":  "my-app",
				},
				[]string{"labels", "wait"},
				[]string{"app"},
			),
			expectedArgs: []string{"deploy", "--labels", "env=production", "--labels", "version=v1.2.3", "--wait", "my-app"},
		},
		{
			name:    "Command with quoted arguments",
			cmdPath: []string{"exec"},
			input: makeFlatInput(
				[]string{"exec"},
				map[string]any{
					"a": "argument with spaces",
					"b": "another quoted arg",
					"c": "normal",
				},
				nil,
				[]string{"a", "b", "c"},
			),
			expectedArgs: []string{"exec", "argument with spaces", "another quoted arg", "normal"},
		},
		{
			// 回归：子命令名本身含下划线时，命令路径必须原样保留。
			// 该路径来自注册期元数据，不得由工具名（下划线是层级分隔符的假设）反解。
			name:         "Command name containing underscore",
			cmdPath:      []string{"get_all"},
			input:        makeFlatInput([]string{"get_all"}, map[string]any{}, nil, nil),
			expectedArgs: []string{"get_all"},
		},
		{
			// 回归：root 名与子命令名都含下划线时，路径仍必须原样保留。
			name:         "Root and sub-command names containing underscore",
			cmdPath:      []string{"sre", "open"},
			input:        makeFlatInput([]string{"sre", "open"}, map[string]any{}, nil, nil),
			expectedArgs: []string{"sre", "open"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildCommandArgs(tt.input)

			// Extract command parts for comparison
			commandParts := len(result) - len(tt.expectedArgs)
			if commandParts >= 0 {
				// Compare command parts
				assert.Equal(t, tt.expectedArgs[:commandParts], result[:commandParts], "Command parts mismatch")
				// Compare flags and args (order might vary for flags)
				assert.ElementsMatch(t, tt.expectedArgs[commandParts:], result[commandParts:], "Flags/args mismatch")
			} else {
				assert.Equal(t, tt.expectedArgs, result)
			}
		})
	}
}
