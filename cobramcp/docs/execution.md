# Tool Execution

> **注意（过时文档）**：本文描述的是嵌套 `flags`/`args` Schema 时代的行为。
> 当前实现为**扁平 Schema**（所有参数为顶层属性），命令路径由注册期元数据还原
> 而非从工具名反解。以 mcp-design.md 与代码注释为准。

When an AI assistant calls an MCP tool, cobramcp executes your CLI as a subprocess.

## Execution Flow

1. **Middleware** (optional) - Wraps execution with custom logic
2. **Command Execution** - Spawns CLI subprocess, captures output

## Command Construction

MCP tool calls become CLI invocations:

**Input (flat: flags and positional arguments are all top-level keys):**
```json
{
  "name": "kubectl_get_pods",
  "arguments": {
    "namespace": "production",
    "output": "json",
    "name": "web-server"
  }
}
```

**Constructed:**
```bash
/path/to/kubectl get pods --namespace production --output json web-server
```

**Flag conversion:**
- Boolean: `true` → `--flag`, `false` → omitted
- String/numeric: `--flag value`
- Arrays: `--flag a --flag b`
- Null/empty: omitted

## Output

All executions return:

```json
{
  "stdout": "command output...",
  "stderr": "error messages...",
  "exitCode": 0
}
```

Non-zero exit codes indicate command errors (not execution failures).

## Cancellation

Execution can be cancelled by:
- Middleware returning early without calling next
- MCP client cancelling request
- Parent context timeout

Cancelled executions kill the subprocess and return an error.
