module github.com/kalandramo/bald/ai/openai

go 1.27.1

require (
	github.com/kalandramo/bald/bconf v0.7.2
	github.com/sashabaranov/go-openai v1.41.2
)

require google.golang.org/protobuf v1.36.11 // indirect

replace github.com/kalandramo/bald/bconf => ../../bconf
