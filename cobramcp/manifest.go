package cobramcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

const (
	// ManifestResourceURI 是工具清单 Resource 的固定 URI。
	//
	// 支持 Resources 的客户端（例如 Claude Desktop）在 initialize 之后
	// ListResources 就能看到它，一次 ReadResource 即可拿到全部对外暴露工具的
	// 元数据，不必逐轮 tools/list 分页拉取。URI 是确定性的常量：同一份工具集
	// 在任何进程、任何执行模型下都是这一个地址。
	ManifestResourceURI = "tools://manifest"

	// ManifestMIMEType 是清单内容的 MIME 类型。清单是一个 JSON 数组，
	// 每个元素就是 tools/list 里的一个工具对象。
	ManifestMIMEType = "application/json"

	// manifestResourceName 是 Resource 的 name 字段（MCP 要求必有）。
	manifestResourceName = "tool_manifest"

	// manifestResourceDescription 直接告诉模型这份清单是什么、什么时候该用。
	manifestResourceDescription = "Metadata for every MCP tool this server exposes " +
		"(name, description, inputSchema, annotations). Read it once to load the whole tool catalog " +
		"instead of paging through tools/list."
)

// toolManifest 把一组已注册的工具定义编码成清单 JSON。
//
// 单一真相源：入参就是真正注册进服务端的那个 []*mcp.Tool 切片，编码直接复用
// mcp.Tool 自己的 MarshalJSON——tools/list 走的也是它。因此清单里的每个元素与
// 客户端在 tools/list 里看到的是同一个对象，本包没有第二份"元数据构造"代码，
// 也就不可能出现清单与工具定义漂移。
//
// 确定性：按 name 升序重排（注册顺序是命令树深度优先遍历顺序，属于实现细节，
// 不该被客户端依赖），其余键顺序由 mcp.Tool.MarshalJSON 固定，同一份工具集
// 必得逐字节相同的输出。这条保证让客户端可以按字节比对/缓存清单。
func toolManifest(tools []*mcp.Tool) ([]byte, error) {
	ordered := make([]mcp.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}

		ordered = append(ordered, *tool)
	}

	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })

	data, err := json.Marshal(ordered)
	if err != nil {
		return nil, fmt.Errorf("cobramcp: encode tool manifest: %w", err)
	}

	return data, nil
}

// resourceRegistrar 是注册一个 Resource 及其读取处理器的能力。
// transport/mcp 与 mcp-go 的 AddResource 同形，两个执行模型各自传入自己的入口。
type resourceRegistrar func(mcp.Resource, mcpserver.ResourceHandlerFunc) error

// registerManifestResource 把 [ManifestResourceURI] 注册到服务端。
//
// 清单在注册期一次性编码（工具集在服务端生命周期内不变，注册后不支持增删），
// 读取处理器只回同一份字节：不重建、不再遍历命令树、无副作用，因此并发读取
// 天然安全，也不需要任何锁。
//
// 两个执行模型（子进程 [Config] 与进程内 [NewMCPServer]）都调用本函数，所以
// 两边暴露的 Resource 元数据与清单语义逐字段一致；各自传入的 tool 切片就是
// 自己注册工具用的那一份。
func registerManifestResource(register resourceRegistrar, tools []*mcp.Tool) error {
	data, err := toolManifest(tools)
	if err != nil {
		return err
	}

	resource := mcp.NewResource(ManifestResourceURI, manifestResourceName,
		mcp.WithResourceTitle("Tool manifest"),
		mcp.WithResourceDescription(manifestResourceDescription),
		mcp.WithMIMEType(ManifestMIMEType),
		mcp.WithResourceSize(int64(len(data))),
	)

	return register(resource, func(_ context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		// fail-closed：本服务只服务清单这一个 URI，其它 URI 明确报错，
		// 而不是回一份与请求无关的内容让客户端以为读到了。
		if uri := request.Params.URI; uri != ManifestResourceURI {
			return nil, fmt.Errorf("cobramcp: unknown resource %q, this server only serves %q",
				uri, ManifestResourceURI)
		}

		return []mcp.ResourceContents{mcp.TextResourceContents{
			URI:      ManifestResourceURI,
			MIMEType: ManifestMIMEType,
			Text:     string(data),
		}}, nil
	})
}
