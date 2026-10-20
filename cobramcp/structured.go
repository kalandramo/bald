package cobramcp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	baldlog "github.com/kalandramo/bald/log"
)

// maxJSONDataBytes 是一份 stdout 参与 JSON 解析的体积上限。
//
// 超限时只放弃结构化解析、在 warning 里说明，文本轨道依旧完整——引入双轨前
// 的文本本来就没有体积上限，这条限制只避免「同一份大输出再被解析一遍并整体
// 塞进 JSON-RPC 帧」的额外内存与带宽开销，绝不截断任何内容。
const maxJSONDataBytes = 1 << 20

// warningPrefix 让结构化侧的诊断信息一眼能看出是哪条契约没兑现。
const warningPrefix = "outputJSONHint: "

// toolStructuredContent 是工具结果的结构化轨道（structuredContent）。
//
// 它内嵌 [ToolOutput]，因此三个基础字段的 JSON 名与 [ToolOutput] 上 jsonschema
// tag 完全一致（REST 形态 POST /<tool> 的响应体也是这个形状），客户端不需要
// 为 MCP 与 REST 学两套字段名。空字段语义沿用 ToolOutput 的 omitempty：
//
//   - stdout / stderr 为空时**不出现**在 JSON 里。「键缺失」等价于空串，
//     客户端不需要区分 "" 与缺键；
//   - exitCode 恒出现（成功为 0），它是判断成败的主字段——isError 只表示
//     「工具调用是否失败」，非零退出码才是命令自己的失败语义。
//
// 两个可选字段：
//
//   - data：仅当命令用 [AnnotationOutputJSON] 声明了 stdout 是 JSON 契约、
//     且 stdout 真的解析出 JSON 对象或数组时出现；内容是解析后的原值，其中
//     数值是 json.Number，避免 float64 的精度失真（大整数 id 不会被改写）。
//   - warning：结构化侧没产出 data 的原因（非法 JSON、顶层不是对象/数组、
//     超过 maxJSONDataBytes 等）。它只是「契约没兑现」的诊断，**不**把工具
//     调用变成错误——见 toolOutputToResult 的取舍说明。
type toolStructuredContent struct {
	ToolOutput

	Data    any    `json:"data,omitempty"`
	Warning string `json:"warning,omitempty"`
}

// toolOutputToResult 把一次命令执行的结果同时投影到两条轨道：
//
//   - Content（文本轨道）：stdout 与 stderr 用 "\n" 拼接，非零退出码并入
//     mcp.NewToolResultError。这一部分与引入结构化输出之前逐字节一致——既有
//     客户端、既有测试看到的文本不变，是向后兼容的硬承诺。
//   - StructuredContent（结构化轨道）：{stdout, stderr, exitCode} 三字段与
//     [ToolOutput] 对齐，命中 [AnnotationOutputJSON] 的命令额外带 data。
//
// 关于错误路径的不对称取舍：mcp-go v1.1.0 的 CallToolResult 允许 isError 与
// structuredContent 并存（见 mcp/tools.go 的字段定义与其 MarshalJSON），因此
// 本函数在 ExitCode != 0 时**既**返回 isError=true 的文本结果、也照常附上
// 结构化轨道，让模型既能看出失败、又能读到失败命令输出的 JSON 诊断对象。
//
// 关于 data 在错误路径上的行为：只要命令声明了 JSON 契约，无论退出码是否为 0
// 都尝试解析——失败的命令往往正是用 JSON 讲清楚失败原因的那一个，没有必要在
// 这里制造第二套语义。解析失败仅产生 warning，绝不升级为工具错误：工具调用
// 本身可能是成功的（退出码 0，只是输出不是合法 JSON），此时把结果标成 isError
// 会让调用方误以为命令执行失败而重试。
func toolOutputToResult(output ToolOutput, meta toolMeta) *mcp.CallToolResult {
	structured := toolStructuredContent{ToolOutput: output}
	if meta.outputJSON {
		structured.Data, structured.Warning = parseJSONStdout(output.StdOut, meta)
	}

	combined := output.StdOut
	if output.StdErr != "" {
		if combined != "" {
			combined += "\n"
		}
		combined += output.StdErr
	}

	if output.ExitCode != 0 {
		if combined == "" {
			combined = fmt.Sprintf("exit code %d", output.ExitCode)
		}

		result := mcp.NewToolResultError(combined)
		result.StructuredContent = structured

		return result
	}

	result := mcp.NewToolResultText(combined)
	result.StructuredContent = structured

	return result
}

// parseJSONStdout 把声明了 JSON 契约的命令的 stdout 解析成 structuredContent.data。
//
// 返回的 data 只有在 stdout 是一份**完整的** JSON 对象或数组时才非 nil：
// 尾随杂字符、顶层标量（字符串/数字/布尔/null）都不算契约兑现，因为把它们塞进
// data 只会让模型误以为拿到了结构。解析不了的每一种情况都翻译成一句 warning，
// 并且总是保留文本轨道。
//
// 空 stdout（含纯空白）视为「本次没有输出」，既不给 data 也不给 warning：
// 这不是契约违约，只是一个成功但无输出的调用（例如删除类命令）。
func parseJSONStdout(stdout string, meta toolMeta) (data any, warning string) {
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return nil, ""
	}

	if len(trimmed) > maxJSONDataBytes {
		return nil, warnJSONParse(meta, fmt.Sprintf(
			"stdout is %d bytes, above the %d-byte structured parse limit; data omitted",
			len(trimmed), maxJSONDataBytes))
	}

	dec := json.NewDecoder(strings.NewReader(trimmed))
	// UseNumber 保留数值字面量，避免 float64 把大整数改写成另一个数。
	dec.UseNumber()

	var decoded any
	if err := dec.Decode(&decoded); err != nil {
		return nil, warnJSONParse(meta, "stdout is not valid JSON: "+err.Error())
	}

	if dec.More() {
		return nil, warnJSONParse(meta, "stdout has trailing data after the JSON value; data omitted")
	}

	switch decoded.(type) {
	case map[string]any, []any:
		return decoded, ""
	}

	return nil, warnJSONParse(meta, fmt.Sprintf(
		"stdout is valid JSON but of type %s, not an object or array; data omitted",
		jsonKind(decoded)))
}

// jsonKind 把解析后的值翻译成人能读的类型名（%T 对 json.Number 只会打印
// "json.Number"，对 nil 打印 "<nil>"）。
func jsonKind(v any) string {
	switch v := v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// warnJSONParse 记录并返回一句结构化侧诊断。
//
// 契约没兑现需要运维可见（否则「注解写了、数据却一直缺」会静默很久），
// 但它不是错误，所以走 Warn 而非 Error。
func warnJSONParse(meta toolMeta, reason string) string {
	baldlog.Warn(nil, "cobramcp: structured output skipped", "cmd", strings.Join(meta.cmdPath, " "), "reason", reason)

	return warningPrefix + reason
}
