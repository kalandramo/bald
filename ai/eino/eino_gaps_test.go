package eino

import (
	"context"
	"errors"
	"testing"
	"time"

	einoOpenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/document"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/indexer"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	jsonschema "github.com/eino-contrib/jsonschema"
)

// fakes 只满足 interface 形状；本包是透传层，构造成功即达目的，不驱动真实调用。

type fakeChatModel struct{}

func (fakeChatModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "ok"}, nil
}

func (fakeChatModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not implemented")
}

type fakeTemplate struct{}

func (fakeTemplate) Format(context.Context, map[string]any, ...prompt.Option) ([]*schema.Message, error) {
	return nil, nil
}

type fakeEmbedder struct{}

func (fakeEmbedder) EmbedStrings(context.Context, []string, ...embedding.Option) ([][]float64, error) {
	return nil, nil
}

type fakeRetriever struct{}

func (fakeRetriever) Retrieve(context.Context, string, ...retriever.Option) ([]*schema.Document, error) {
	return nil, nil
}

type fakeLoader struct{}

func (fakeLoader) Load(context.Context, document.Source, ...document.LoaderOption) ([]*schema.Document, error) {
	return nil, nil
}

type fakeTransformer struct{}

func (fakeTransformer) Transform(context.Context, []*schema.Document, ...document.TransformerOption) ([]*schema.Document, error) {
	return nil, nil
}

type fakeIndexer struct{}

func (fakeIndexer) Store(context.Context, []*schema.Document, ...indexer.Option) ([]string, error) {
	return nil, nil
}

type fakeTool struct{}

func (fakeTool) Info(context.Context) (*schema.ToolInfo, error) {
	return NewToolInfo("fake", "fake tool", nil), nil
}

func (fakeTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "ok", nil
}

func TestNewChatModel_Validation(t *testing.T) {
	ctx := context.Background()

	if _, err := NewChatModel(ctx, nil); err == nil {
		t.Error("nil config must fail")
	}
	if _, err := NewChatModel(ctx, &Config{Type: ModelType(9)}); err == nil {
		t.Error("unsupported type must fail")
	}
	if _, err := NewChatModel(ctx, &Config{Type: ModelTypeCloud}); err == nil {
		t.Error("cloud type without cloud segment must fail")
	}
	if _, err := NewChatModel(ctx, &Config{Type: ModelTypeLocal}); err == nil {
		t.Error("local type without local segment must fail")
	}
}

// 云端/本地构造都只建客户端，不发请求；配置修饰器在构造前拿到可改的 config。
func TestNewChatModel_Constructors(t *testing.T) {
	ctx := context.Background()

	var seen *einoOpenai.ChatModelConfig
	cloud, err := NewChatModel(ctx, &Config{
		Type:           ModelTypeCloud,
		ModelName:      "gpt-4o-mini",
		TimeoutSeconds: 5,
		Cloud:          &CloudConfig{ApiKey: "sk-test", BaseUrl: "http://127.0.0.1:1/v1"},
	}, WithConfigModifier(func(c *einoOpenai.ChatModelConfig) {
		seen = c
		c.Model = "modified-model"
	}))
	if err != nil {
		t.Fatalf("cloud construct: %v", err)
	}
	if cloud == nil {
		t.Error("cloud model is nil")
	}
	if seen == nil {
		t.Fatal("config modifier not invoked")
	}
	if seen.Model != "modified-model" || seen.Timeout != 5*time.Second {
		t.Errorf("modified config = %+v", seen)
	}
	if seen.BaseURL != "http://127.0.0.1:1/v1" {
		t.Errorf("BaseURL = %q", seen.BaseURL)
	}

	local, err := NewChatModel(ctx, &Config{
		Type:      ModelTypeLocal,
		ModelName: "llama3",
		Local:     &LocalConfig{Host: "127.0.0.1", Port: 11434},
	}, WithConfigModifier(func(c *einoOpenai.ChatModelConfig) {
		seen = c
	}))
	if err != nil {
		t.Fatalf("local construct: %v", err)
	}
	if local == nil {
		t.Error("local model is nil")
	}
	if seen.BaseURL != "http://127.0.0.1:11434/v1" || seen.APIKey != "ollama" {
		t.Errorf("local config = %+v", seen)
	}
	if seen.Timeout != defaultTimeout {
		t.Errorf("Timeout = %v, want default %v", seen.Timeout, defaultTimeout)
	}
}

func TestConfigBuilders(t *testing.T) {
	if _, err := newCloudChatModelConfig(&Config{}); err == nil {
		t.Error("nil cloud segment must fail")
	}
	cfg, err := newCloudChatModelConfig(&Config{ModelName: "m", TimeoutSeconds: 1, Cloud: &CloudConfig{}})
	if err != nil {
		t.Fatalf("cloud config: %v", err)
	}
	if cfg.BaseURL != "" || cfg.Timeout != time.Second {
		t.Errorf("cloud config = %+v", cfg)
	}

	if _, err := newLocalChatModelConfig(&Config{}); err == nil {
		t.Error("nil local segment must fail")
	}
	local, err := newLocalChatModelConfig(&Config{ModelName: "m", Local: &LocalConfig{}})
	if err != nil {
		t.Fatalf("local config: %v", err)
	}
	if local.BaseURL != "http://localhost:11434/v1" {
		t.Errorf("default local BaseURL = %q", local.BaseURL)
	}
}

func TestOptions(t *testing.T) {
	if o := applyOptions(nil); o.configModifier != nil {
		t.Error("zero options must leave modifier nil")
	}

	called := false
	modifier := func(*einoOpenai.ChatModelConfig) { called = true }
	o := applyOptions([]Option{WithConfigModifier(modifier)})
	cfg := &einoOpenai.ChatModelConfig{}
	if got := applyConfigModifier(cfg, o); got != cfg {
		t.Error("applyConfigModifier must return the same config")
	}
	if !called {
		t.Error("config modifier not called")
	}

	if got := applyConfigModifier(cfg, &options{}); got != cfg {
		t.Error("nil modifier must return the same config")
	}
}

func TestToolHelpers(t *testing.T) {
	info := NewToolInfo("search", "search things", nil)
	if info.Name != "search" || info.Desc != "search things" || info.ParamsOneOf != nil {
		t.Errorf("tool info = %+v", info)
	}

	params := NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
		"q": NewParameterInfo(schema.String, "query", true),
	})
	if params == nil {
		t.Error("params oneOf from params is nil")
	}
	if NewParamsOneOfByJSONSchema(&jsonschema.Schema{}) == nil {
		t.Error("params oneOf from JSON schema is nil")
	}

	pi := NewParameterInfo(schema.String, "color", false,
		WithEnum([]string{"red", "blue"}),
		WithSubParams(map[string]*schema.ParameterInfo{}),
		WithElemInfo(NewParameterInfo(schema.Integer, "n", false)),
	)
	if pi.Type != schema.String || pi.Desc != "color" || pi.Required {
		t.Errorf("parameter info = %+v", pi)
	}
	if len(pi.Enum) != 2 || pi.SubParams == nil || pi.ElemInfo == nil {
		t.Errorf("parameter options not applied: %+v", pi)
	}

	opts := []compose.ToolsNodeOption{
		WithToolList(fakeTool{}),
		WithToolOption(),
	}
	if len(opts) != 2 || opts[0] == nil || opts[1] == nil {
		t.Error("tools node options must be constructed")
	}
}

func TestNewToolNode(t *testing.T) {
	node, err := NewToolNode(context.Background(), &compose.ToolsNodeConfig{Tools: []tool.BaseTool{fakeTool{}}})
	if err != nil {
		t.Fatalf("NewToolNode: %v", err)
	}
	if node == nil {
		t.Error("tools node is nil")
	}
}

func TestPromptHelpers(t *testing.T) {
	if m := SystemMessage("sys"); m.Role != schema.System || m.Content != "sys" {
		t.Errorf("system message = %+v", m)
	}
	if m := UserMessage("user"); m.Role != schema.User || m.Content != "user" {
		t.Errorf("user message = %+v", m)
	}
	if m := AssistantMessage("assistant"); m.Role != schema.Assistant || m.Content != "assistant" {
		t.Errorf("assistant message = %+v", m)
	}

	tpl := FromMessages(schema.FString,
		SystemMessage("you are helpful"),
		MessagesPlaceholder("history", true),
		UserMessage("hello {name}"),
	)
	msgs, err := tpl.Format(context.Background(), map[string]any{
		"history": []*schema.Message{{Role: schema.User, Content: "earlier"}},
		"name":    "bob",
	})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("formatted messages = %d, want 3", len(msgs))
	}
	if msgs[2].Content != "hello bob" {
		t.Errorf("formatted user message = %q", msgs[2].Content)
	}
}

func TestChainAndGraphWrappers(t *testing.T) {
	ctx := context.Background()

	chain := NewChain[string, string]()
	if chain == nil {
		t.Fatal("NewChain returned nil")
	}
	chain.AppendLambda(InvokableLambda(func(_ context.Context, in string) (string, error) {
		return in + "!", nil
	}))
	runnable, err := CompileChain(ctx, chain)
	if err != nil {
		t.Fatalf("CompileChain: %v", err)
	}
	out, err := runnable.Invoke(ctx, "hi")
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if out != "hi!" {
		t.Errorf("Invoke = %q, want hi!", out)
	}

	listChain := NewChain[string, []string]()
	listChain.AppendLambda(ToList[string]())
	listRunnable, err := CompileChain(ctx, listChain)
	if err != nil {
		t.Fatalf("CompileChain(list): %v", err)
	}
	list, err := listRunnable.Invoke(ctx, "x")
	if err != nil {
		t.Fatalf("Invoke(list): %v", err)
	}
	if len(list) != 1 || list[0] != "x" {
		t.Errorf("ToList result = %v, want [x]", list)
	}

	if NewGraph[string, string]() == nil {
		t.Error("NewGraph returned nil")
	}
	if NewWorkflow[string, string]() == nil {
		t.Error("NewWorkflow returned nil")
	}
	if NewParallel() == nil {
		t.Error("NewParallel returned nil")
	}
	if NewChainBranch(func(_ context.Context, _ string) (string, error) { return "next", nil }) == nil {
		t.Error("NewChainBranch returned nil")
	}
	if WithGenLocalState(func(context.Context) int { return 1 }) == nil {
		t.Error("WithGenLocalState returned nil")
	}
}

func TestLambdaConstructors(t *testing.T) {
	if StreamableLambda(func(context.Context, string) (*schema.StreamReader[string], error) {
		return nil, nil
	}) == nil {
		t.Error("StreamableLambda returned nil")
	}
	if CollectableLambda(func(context.Context, *schema.StreamReader[string]) (string, error) {
		return "", nil
	}) == nil {
		t.Error("CollectableLambda returned nil")
	}
	if TransformableLambda(func(context.Context, *schema.StreamReader[string]) (*schema.StreamReader[string], error) {
		return nil, nil
	}) == nil {
		t.Error("TransformableLambda returned nil")
	}

	lambda, err := AnyLambda[string, string, any](
		func(_ context.Context, in string, _ ...any) (string, error) { return in, nil },
		nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("AnyLambda: %v", err)
	}
	if lambda == nil {
		t.Error("AnyLambda returned nil lambda")
	}
}

// 八个 Append* 包装必须原样返回传入的 Chain（链式语义）。
func TestChainAppendWrappers(t *testing.T) {
	ctx := context.Background()
	toolsNode, err := NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: []tool.BaseTool{fakeTool{}}})
	if err != nil {
		t.Fatalf("NewToolNode: %v", err)
	}

	chain := NewChain[[]*schema.Message, *schema.Message]()
	if got := AppendChatModel(chain, fakeChatModel{}); got != chain {
		t.Error("AppendChatModel must return the chain")
	}
	if got := AppendChatTemplate(chain, fakeTemplate{}); got != chain {
		t.Error("AppendChatTemplate must return the chain")
	}
	if got := AppendToolsNode(chain, toolsNode); got != chain {
		t.Error("AppendToolsNode must return the chain")
	}
	if got := AppendEmbedding(chain, fakeEmbedder{}); got != chain {
		t.Error("AppendEmbedding must return the chain")
	}
	if got := AppendRetriever(chain, fakeRetriever{}); got != chain {
		t.Error("AppendRetriever must return the chain")
	}
	if got := AppendLoader(chain, fakeLoader{}); got != chain {
		t.Error("AppendLoader must return the chain")
	}
	if got := AppendDocumentTransformer(chain, fakeTransformer{}); got != chain {
		t.Error("AppendDocumentTransformer must return the chain")
	}
	if got := AppendIndexer(chain, fakeIndexer{}); got != chain {
		t.Error("AppendIndexer must return the chain")
	}
}

func TestStartEndConstants(t *testing.T) {
	if START != compose.START || END != compose.END {
		t.Errorf("START/END = %q/%q, want compose values", START, END)
	}
}
