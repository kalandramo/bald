package cobramcp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"

	"github.com/mark3labs/mcp-go/mcp"

	baldlog "github.com/kalandramo/bald/log"
)

// executablePath holds the absolute path to the running binary. It is resolved
// once at startup and used by the subprocess-based [execute] function to
// re-invoke the same binary when serving tools through the legacy [Config] API.
var executablePath = initExecPath()

// initExecPath resolves the path of the currently running executable.
// It panics on error because a missing executable path means the process
// cannot re-invoke itself, which is a fatal misconfiguration.
func initExecPath() string {
	path, err := os.Executable()
	if err != nil {
		panic(fmt.Sprintf("failed to get executable path: %v", err))
	}

	return path
}

// execSubprocess re-invokes the current binary with args, captures
// stdout/stderr, and returns a ToolOutput.
//
// Non-zero exit codes are surfaced in ToolOutput.ExitCode rather than as Go
// errors. A Go error is only returned if the subprocess cannot be launched at
// all (e.g. the binary is not found or lacks execute permission).
func execSubprocess(ctx context.Context, args []string) (ToolOutput, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, executablePath, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	exitCode := 0

	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// The process ran but exited with a non-zero status; surface the
			// exit code in the output rather than as a Go error.
			exitCode = exitErr.ExitCode()
		} else {
			// A non-exit error means the subprocess could not start at all.
			return ToolOutput{}, err
		}
	}

	return ToolOutput{
		StdOut:   stdout.String(),
		StdErr:   stderr.String(),
		ExitCode: exitCode,
	}, nil
}

// execute runs the CLI tool by re-invoking the current binary as a subprocess.
//
// This is the execution model used by the original [Config]-based API. The MCP
// server process re-launches itself with the decoded command arguments, captures
// stdout/stderr, and returns them in a [ToolOutput].
func execute(ctx context.Context, request mcp.CallToolRequest, input ToolInput) (*mcp.CallToolResult, ToolOutput, error) {
	name := request.Params.Name
	baldlog.Info(ctx, "subprocess MCP tool request received", "tool", name)

	args := buildCommandArgs(input)
	baldlog.Debug(ctx, "executing subprocess command", "tool", name, "input", input, "args", args)

	output, err := execSubprocess(ctx, args)
	if err != nil {
		baldlog.Error(ctx, "subprocess failed to launch", "tool", name, "error", err)
		return nil, ToolOutput{}, err
	}
	return nil, output, nil
}

// buildCommandArgs constructs CLI arguments from the MCP request.
// It starts from the command path recorded at registration time and appends
// flags and positional arguments so the resulting slice can be passed directly
// to exec.Command when re-invoking the binary as a subprocess.
//
// The command path comes from input.CmdPath (see toolMeta.cmdPath) and is NOT
// reverse-engineered from the tool name: tool names encode spaces as
// underscores, which is not reversible when a command name itself contains an
// underscore.
//
// The flat ToolInput is split into flags (using FlagNames) and positional
// arguments (using ArgNames, which preserves cmd.Use ordering).
func buildCommandArgs(input ToolInput) []string {
	// Start from the registration-time command path.
	args := make([]string, len(input.CmdPath))
	copy(args, input.CmdPath)

	// Split flat input into flags and positional args.
	flagMap, posArgs := splitFlatInput(input)

	// Add flags.
	args = append(args, buildFlagArgs(flagMap)...)

	// Add positional arguments in spec order.
	return append(args, posArgs...)
}

// splitFlatInput separates a flat ToolInput into a flags map and an ordered
// positional-argument slice.
//
// Flags are identified by FlagNames; remaining entries in FlatInput that match
// ArgNames are collected in ArgNames order as positional arguments.
func splitFlatInput(input ToolInput) (flagMap map[string]any, posArgs []string) {
	flagMap = make(map[string]any)

	// Collect flag values.
	for k, v := range input.FlatInput {
		if _, isFlag := input.FlagNames[k]; isFlag {
			flagMap[k] = v
		}
	}

	// Collect positional args in the declared order.
	for _, name := range input.ArgNames {
		val, ok := input.FlatInput[name]
		if !ok || val == nil {
			continue
		}
		switch v := val.(type) {
		case []any:
			for _, item := range v {
				posArgs = append(posArgs, fmt.Sprintf("%v", item))
			}
		case []string:
			posArgs = append(posArgs, v...)
		default:
			posArgs = append(posArgs, fmt.Sprintf("%v", val))
		}
	}

	return flagMap, posArgs
}

// buildFlagArgs converts a flags map into a slice of CLI flag arguments
// understood by cobra/pflag.
//
// Conversion rules:
//   - bool true  → "--flag"       (false is omitted)
//   - scalar     → "--flag value"
//   - []any      → "--flag a" "--flag b" (repeated, one per element)
//   - map[string]any → "--flag key=value" (stringToString style)
func buildFlagArgs(flagMap map[string]any) []string {
	var args []string

	// Iterate flag names in sorted order so the generated argument slice is
	// deterministic. Map iteration order is randomized in Go; without this,
	// the same input yields different argument orderings across runs, which
	// makes logs, traces and failure reproductions needlessly hard to compare.
	// (pflag itself is order-insensitive for distinct flags.)
	names := make([]string, 0, len(flagMap))
	for name := range flagMap {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		value := flagMap[name]
		if name == "" || value == nil {
			continue
		}

		// Slice: emit one --flag per element (pflag slice / repeated flags).
		if items, ok := value.([]any); ok {
			for _, item := range items {
				args = append(args, parseFlagArgValue(name, item)...)
			}

			continue
		}

		// Map: emit --flag key=value entries (pflag stringToString).
		if mapVal, ok := value.(map[string]any); ok {
			for k, v := range mapVal {
				args = append(args, fmt.Sprintf("--%s", name), fmt.Sprintf("%s=%v", k, v))
			}

			continue
		}

		args = append(args, parseFlagArgValue(name, value)...)
	}

	return args
}

// parseFlagArgValue converts a single flag value to CLI argument tokens.
// A bool true yields ["--name"]; any other type yields ["--name", "value"].
// A bool false or nil value yields an empty slice (flag is omitted).
func parseFlagArgValue(name string, value any) (retVal []string) {
	if value != nil {
		switch v := value.(type) {
		case bool:
			if v {
				retVal = append(retVal, fmt.Sprintf("--%s", name))
			}
		default:
			retVal = append(retVal, fmt.Sprintf("--%s", name), fmt.Sprintf("%v", value))
		}
	}

	return retVal
}
