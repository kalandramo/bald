package openai

// ModelType specifies whether the model runs locally or in the cloud.
type ModelType int32

const (
	ModelTypeLocal ModelType = 1 // local model (e.g. Ollama)
	ModelTypeCloud ModelType = 2 // cloud model (OpenAI-compatible API)
)

// CloudConfig holds settings for cloud-based LLM providers.
type CloudConfig struct {
	ApiKey       string
	BaseUrl      string
	Organization string
}

// LocalConfig holds settings for locally-hosted models.
type LocalConfig struct {
	Host string
	Port int32
}

// Config is the configuration consumed by NewClient.
type Config struct {
	Type           ModelType
	ModelName      string
	TimeoutSeconds int32

	Cloud *CloudConfig
	Local *LocalConfig
}
