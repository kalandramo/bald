package langchaingo_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/memory"
	"github.com/tmc/langchaingo/prompts"
	"github.com/tmc/langchaingo/schema"
	"github.com/tmc/langchaingo/tools"
	"github.com/tmc/langchaingo/vectorstores"

	"github.com/kalandramo/bald/ai/langchaingo"
)

// ---- 测试替身：让纯离线/无网络下也能覆盖各构造与调用转发 ----

// stubModel 满足 llms.Model，仅构造期使用，不会被真正调用。
type stubModel struct{}

func (stubModel) GenerateContent(context.Context, []llms.MessageContent, ...llms.CallOption) (*llms.ContentResponse, error) {
	return &llms.ContentResponse{}, nil
}

func (stubModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	return "ok", nil
}

// stubEmbedder 满足 embeddings.EmbedderClient（单方法 CreateEmbedding）。
type stubEmbedder struct{ err error }

func (s stubEmbedder) CreateEmbedding(_ context.Context, texts []string) ([][]float32, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{float32(i), 1, 2}
	}
	return out, nil
}

// stubStore 满足 vectorstores.VectorStore。
type stubStore struct{ err error }

func (s stubStore) AddDocuments(_ context.Context, docs []schema.Document, _ ...vectorstores.Option) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	ids := make([]string, len(docs))
	for i := range docs {
		ids[i] = "id"
	}
	return ids, nil
}

func (s stubStore) SimilaritySearch(_ context.Context, query string, n int, _ ...vectorstores.Option) ([]schema.Document, error) {
	if s.err != nil {
		return nil, s.err
	}
	return []schema.Document{{PageContent: query}}, nil
}

// TestAgents 覆盖 agent.go 全部构造与 Executor 便捷方法。
func TestAgents(t *testing.T) {
	m := stubModel{}
	tl := []tools.Tool{}
	if a := langchaingo.NewOneShotAgent(m, tl); a == nil {
		t.Fatal("NewOneShotAgent nil")
	}
	if a := langchaingo.NewConversationalAgent(m, tl); a == nil {
		t.Fatal("NewConversationalAgent nil")
	}
	if a := langchaingo.NewOpenAIFunctionsAgent(m, tl); a == nil {
		t.Fatal("NewOpenAIFunctionsAgent nil")
	}
	if e := langchaingo.NewExecutor(langchaingo.NewOneShotAgent(m, tl)); e == nil {
		t.Fatal("NewExecutor nil")
	}
	if e := langchaingo.NewOneShotExecutor(m, tl); e == nil {
		t.Fatal("NewOneShotExecutor nil")
	}
	if e := langchaingo.NewConversationalExecutor(m, tl); e == nil {
		t.Fatal("NewConversationalExecutor nil")
	}
	if e := langchaingo.NewOpenAIFunctionsExecutor(m, tl); e == nil {
		t.Fatal("NewOpenAIFunctionsExecutor nil")
	}
}

// TestChains 覆盖 chain.go 构造；顺序链同时覆盖成功与错误两分支。
func TestChains(t *testing.T) {
	m := stubModel{}
	pt := prompts.NewPromptTemplate("{{.q}", []string{"q"})
	lc := langchaingo.NewLLMChain(m, pt)
	if lc == nil {
		t.Fatal("NewLLMChain nil")
	}
	// NewConversationChain：mem==nil 与 mem!=nil 两分支
	_ = langchaingo.NewConversationChain(m, nil)
	_ = langchaingo.NewConversationChain(m, memory.NewConversationBuffer())
	// NewSequentialChain / NewSimpleSequentialChain 是纯转发（单语句，无内部分支），
	// 各调一次即覆盖；错误与否由上游 chains 库决定，不在此断言。
	_, _ = langchaingo.NewSequentialChain([]chains.Chain{lc}, []string{"q"}, []string{"q"})
	_, _ = langchaingo.NewSimpleSequentialChain([]chains.Chain{lc})
	_ = langchaingo.NewStuffDocumentsChain(lc)
	_ = langchaingo.LoadStuffSummarization(m)
	_ = langchaingo.LoadRefineSummarization(m)
	_ = langchaingo.LoadMapReduceSummarization(m)
}

// TestNewModel 覆盖 client.go 全部配置分支。
func TestNewModel(t *testing.T) {
	if _, err := langchaingo.NewModel(nil); err == nil {
		t.Fatal("nil 配置应报错")
	}
	if _, err := langchaingo.NewModel(&langchaingo.Config{Type: langchaingo.ModelType(99)}); err == nil {
		t.Fatal("非法类型应报错")
	}
	// cloud nil 子配置
	if _, err := langchaingo.NewModel(&langchaingo.Config{Type: langchaingo.ModelTypeCloud}); err == nil {
		t.Fatal("cloud 子配置 nil 应报错")
	}
	// cloud 完整 + 自定义 openai 选项 + 自定义超时（覆盖 newCloudModel 的 TimeoutSeconds>0 分支）
	if _, err := langchaingo.NewModel(&langchaingo.Config{
		Type:           langchaingo.ModelTypeCloud,
		ModelName:      "gpt-4",
		TimeoutSeconds: 5,
		Cloud: &langchaingo.CloudConfig{
			ApiKey:       "k",
			BaseUrl:      "https://x/v1",
			Organization: "o",
		},
	}, langchaingo.WithOpenaiOptions()); err != nil {
		t.Fatalf("cloud 模型构造失败: %v", err)
	}
	// local nil 子配置
	if _, err := langchaingo.NewModel(&langchaingo.Config{Type: langchaingo.ModelTypeLocal}); err == nil {
		t.Fatal("local 子配置 nil 应报错")
	}
	// local 默认 host/port
	if _, err := langchaingo.NewModel(&langchaingo.Config{
		Type:           langchaingo.ModelTypeLocal,
		TimeoutSeconds: 7,
		Local:          &langchaingo.LocalConfig{},
	}); err != nil {
		t.Fatalf("local 默认模型构造失败: %v", err)
	}
	// local 自定义 host/port + 自定义 HTTPClient + ollama 选项
	if _, err := langchaingo.NewModel(&langchaingo.Config{
		Type:  langchaingo.ModelTypeLocal,
		Local: &langchaingo.LocalConfig{Host: "1.2.3.4", Port: 9999},
	}, langchaingo.WithHTTPClient(&http.Client{}), langchaingo.WithOllamaOptions()); err != nil {
		t.Fatalf("local 自定义模型构造失败: %v", err)
	}
}

// TestEmbedding 覆盖 embedding.go：构造 + EmbedQuery/EmbedDocuments 成功与错误传递。
func TestEmbedding(t *testing.T) {
	ctx := context.Background()
	e, err := langchaingo.NewEmbedder(stubEmbedder{}, langchaingo.WithStripNewLines(true), langchaingo.WithBatchSize(2))
	if err != nil || e == nil {
		t.Fatalf("NewEmbedder 失败: %v", err)
	}
	// 成功
	if v, err := langchaingo.EmbedQuery(ctx, stubEmbedder{}, "hello"); err != nil || len(v) == 0 {
		t.Fatalf("EmbedQuery 成功路径失败: %v", err)
	}
	if vs, err := langchaingo.EmbedDocuments(ctx, stubEmbedder{}, []string{"a", "b"}); err != nil || len(vs) != 2 {
		t.Fatalf("EmbedDocuments 成功路径失败: %v", err)
	}
	// 底层返回错误时透传
	if _, err := langchaingo.EmbedQuery(ctx, stubEmbedder{err: errors.New("boom")}, "x"); err == nil {
		t.Fatal("期望 EmbedQuery 透传底层错误")
	}
	if _, err := langchaingo.EmbedDocuments(ctx, stubEmbedder{err: errors.New("boom")}, []string{"x"}); err == nil {
		t.Fatal("期望 EmbedDocuments 透传底层错误")
	}
}

// TestMemory 覆盖 memory.go 全部构造与选项。
func TestMemory(t *testing.T) {
	if langchaingo.NewChatMessageHistory() == nil {
		t.Fatal("NewChatMessageHistory nil")
	}
	if langchaingo.NewConversationBuffer(
		langchaingo.WithReturnMessages(true),
		langchaingo.WithMemoryKey("h"),
		langchaingo.WithInputKey("i"),
		langchaingo.WithOutputKey("o"),
		langchaingo.WithHumanPrefix("Human"),
		langchaingo.WithAIPrefix("AI"),
		langchaingo.WithChatHistory(memory.NewChatMessageHistory()),
	) == nil {
		t.Fatal("NewConversationBuffer nil")
	}
	if langchaingo.NewConversationWindowBuffer(5) == nil {
		t.Fatal("NewConversationWindowBuffer nil")
	}
	if langchaingo.NewConversationTokenBuffer(stubModel{}, 1000) == nil {
		t.Fatal("NewConversationTokenBuffer nil")
	}
	_ = langchaingo.NewSimpleMemory()
}

// TestVectorStore 覆盖 vectorstore.go：转发调用 + 选项构造。
func TestVectorStore(t *testing.T) {
	ctx := context.Background()
	st := stubStore{}
	_ = langchaingo.ToRetriever(st, 3)
	ids, err := langchaingo.AddDocuments(ctx, st, []schema.Document{{PageContent: "x"}},
		langchaingo.WithNameSpace("ns"),
		langchaingo.WithScoreThreshold(0.5),
		langchaingo.WithFilters(nil),
		langchaingo.WithDeduplicater(func(context.Context, schema.Document) bool { return false }),
	)
	if err != nil {
		t.Fatalf("AddDocuments 失败: %v", err)
	}
	_ = ids
	docs, err := langchaingo.SimilaritySearch(ctx, st, "q", 1)
	if err != nil {
		t.Fatalf("SimilaritySearch 失败: %v", err)
	}
	_ = docs
	// WithEmbedder 单独构造（需要 embeddings.Embedder）
	emb, _ := langchaingo.NewEmbedder(stubEmbedder{})
	_ = langchaingo.WithEmbedder(emb)
}
