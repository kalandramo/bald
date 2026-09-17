# Tool Execution

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
