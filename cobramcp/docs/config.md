# Configuration

## Selectors

Selectors control which commands and flags become MCP tools. cobramcp evaluates selectors in order and uses the **first matching selector** for each command. If no selectors match a command, the command is not exposed as a MCP tool.

### Default Behavior

- If `Config.Selectors` is nil/empty, all commands and flags are exposed
- If `Config.DefaultEnv` is nil, no default environment variables are added to editor configs
- If `CmdSelector` is nil, the selector matches all commands
- If `LocalFlagSelector` or `InheritedFlagSelector` is nil, all flags are included

### Automatic Filtering

Always filtered regardless of configuration:

- Hidden and deprecated commands/flags
- Non-runnable commands (no Run/RunE)
- Built-in commands (the cobramcp command, help, completion). The cobramcp command name defaults to "mcp" but can be changed via Config.CommandName

## DefaultEnv

Editors like VSCode and Cursor launch MCP server subprocesses with a minimal environment. On macOS this typically means a PATH of just `/usr/bin:/bin:/usr/sbin:/sbin`, which cannot find executables managed by mise, asdf, homebrew, nix, or installed to non-standard paths.

`DefaultEnv` specifies environment variables that are automatically included when `enable` writes a server config for any editor. These are merged with user-provided `--env` values; user values take precedence on conflict.

### Capture PATH

The most common use is to capture the current shell's PATH so the MCP server subprocess can find tools like `helm`, `kubectl`, `docker`, `terraform`, etc.:

```go
config := &cobramcp.Config{
    DefaultEnv: map[string]string{
        "PATH": os.Getenv("PATH"),
    },
}
```

### Multiple Variables

```go
config := &cobramcp.Config{
    DefaultEnv: map[string]string{
        "PATH":       os.Getenv("PATH"),
        "KUBECONFIG": os.Getenv("KUBECONFIG"),
        "HOME":       os.Getenv("HOME"),
    },
}
```

### User Override

User-provided `--env` values always take precedence over `DefaultEnv`:

```bash
# Uses DefaultEnv PATH
./my-cli mcp vscode enable

# Overrides DefaultEnv PATH with user value, keeps other DefaultEnv vars
./my-cli mcp vscode enable --env PATH=/custom/path
```

## Examples

### Expose Specific Commands

```go
config := &cobramcp.Config{
    Selectors: []cobramcp.Selector{
        {
            CmdSelector: cobramcp.AllowCmds("kubectl get", "kubectl describe"),
        },
    },
}
```

### Exclude Sensitive Flags

```go
config := &cobramcp.Config{
    Selectors: []cobramcp.Selector{
        {
            LocalFlagSelector: cobramcp.ExcludeFlags("token", "password"),
            InheritedFlagSelector: cobramcp.NoFlags,
        },
    },
}
```

### Different Rules per Command Type

```go
config := &cobramcp.Config{
    Selectors: []cobramcp.Selector{
        {
            // Read ops: timeout
            CmdSelector: cobramcp.AllowCmdsContaining("get", "list"),
            Middleware: func(ctx context.Context, req mcp.CallToolRequest, in cobramcp.ToolInput, next func(context.Context, mcp.CallToolRequest, cobramcp.ToolInput) (*mcp.CallToolResult, cobramcp.ToolOutput, error)) (*mcp.CallToolResult, cobramcp.ToolOutput, error) {
                ctx, cancel := context.WithTimeout(ctx, time.Minute)
                defer cancel()
                return next(ctx, req, in)
            },
        },
        {
            // Write ops: restrict flags
            CmdSelector: cobramcp.AllowCmdsContaining("delete", "apply"),
            LocalFlagSelector: cobramcp.ExcludeFlags("force", "all"),
        },
    },
}
```

## Selector Functions

### Commands

- `AllowCmds(cmds ...string)` - Exact matches
- `ExcludeCmds(cmds ...string)` - Exact exclusions
- `AllowCmdsContaining(substrings ...string)` - Contains any
- `ExcludeCmdsContaining(substrings ...string)` - Excludes all

### Flags

- `AllowFlags(names ...string)` - Include only these
- `ExcludeFlags(names ...string)` - Exclude these
- `NoFlags` - Exclude all

### Custom Selector Functions

```go
config := &cobramcp.Config{
    Selectors: []cobramcp.Selector{
        {
            CmdSelector: func(cmd *cobra.Command) bool {
                // Only expose commands that have been annotated as "mcp"
                return cmd.Annotations["mcp"] == "true"
            },

            LocalFlagSelector: func(flag *pflag.Flag) bool {
                return flag.Annotations["mcp"] == "true"
            },

            InheritedFlagSelector: func(flag *pflag.Flag) bool {
                return flag.Annotations["mcp"] == "true"
            },
        },
    },
}
```

## Middleware

Wrap execution with custom logic:

```go
Middleware: func(ctx context.Context, req mcp.CallToolRequest, in cobramcp.ToolInput, next func(context.Context, mcp.CallToolRequest, cobramcp.ToolInput) (*mcp.CallToolResult, cobramcp.ToolOutput, error)) (*mcp.CallToolResult, cobramcp.ToolOutput, error) {
    // Pre-execution: Add timeout
    ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
    defer cancel()

    // Pre-execution: Validate input
    if len(in.Args) > 10 {
        in.Args = in.Args[:10]
    }

    // Execute the command
    res, out, err := next(ctx, req, in)

    // Post-execution: Filter output
    if strings.Contains(out.StdOut, "SECRET") {
        out.StdOut = "[REDACTED]"
    }

    return res, out, err
}
```

## Tool Annotations

Set MCP [tool annotations](https://modelcontextprotocol.io/specification/2025-06-18/server/tools#tool-annotations) on Cobra commands using `cmd.Annotations`. These hints help AI clients make informed decisions about tool invocation.

### Available Annotation Keys

| Key                                                 | Type   | Description                              |
| --------------------------------------------------- | ------ | ---------------------------------------- |
| `cobramcp.AnnotationTitle` (`"title"`)                 | string | Human-readable title for the tool        |
| `cobramcp.AnnotationReadOnly` (`"readOnlyHint"`)       | bool   | Tool does not modify its environment     |
| `cobramcp.AnnotationDestructive` (`"destructiveHint"`) | bool   | Tool may perform destructive updates     |
| `cobramcp.AnnotationIdempotent` (`"idempotentHint"`)   | bool   | Repeated calls have no additional effect |
| `cobramcp.AnnotationOpenWorld` (`"openWorldHint"`)     | bool   | Tool may interact with external entities |

Boolean values are parsed with `strconv.ParseBool` (`"true"`, `"1"`, `"t"`, `"false"`, `"0"`, `"f"`, etc.). Invalid values are skipped with a warning.

### Example

```go
listCmd := &cobra.Command{
    Use:   "list",
    Short: "List all resources",
    Annotations: map[string]string{
        cobramcp.AnnotationTitle:    "List Resources",
        cobramcp.AnnotationReadOnly: "true",
    },
}

deleteCmd := &cobra.Command{
    Use:   "delete [id]",
    Short: "Delete a resource",
    Annotations: map[string]string{
        cobramcp.AnnotationTitle:       "Delete Resource",
        cobramcp.AnnotationDestructive: "true",
        cobramcp.AnnotationIdempotent:  "true",
        cobramcp.AnnotationOpenWorld:   "false",
    },
}
```

## Logging

cobramcp 不持有日志后端配置——所有日志经 [`bald/log`](../../log) 契约输出。

- **进程内模型**（`NewMCPServer`）：使用宿主装配的 Logger（未装配时为静默 nop），由宿主决定级别与输出目标。
- **自带 CLI 子命令**（`mcp start/stream/rest/tools`）：运行期安装 stderr 后端，可用 `--log-level` 控制级别。stdio 模式下 **stdout 是 MCP 协议通道**，日志必须走 stderr。

```bash
./my-cli mcp start --log-level debug
```
