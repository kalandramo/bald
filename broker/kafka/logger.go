package kafka

import (
	"fmt"

	kafkaGo "github.com/segmentio/kafka-go"
	"github.com/kalandramo/bald/log"
)

const logKey = "[kafka]"

func logErrorf(format string, args ...any) {
	log.Error(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}

// Logger adapts to kafka-go.Logger interface
type Logger struct{}

func (l Logger) Printf(msg string, args ...any) {
	log.Info(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprintf(msg, args...)))
}

// ErrorLogger adapts to kafka-go.Logger interface for errors
type ErrorLogger struct{}

func (l ErrorLogger) Printf(msg string, args ...any) {
	log.Error(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprintf(msg, args...)))
}

var _ kafkaGo.Logger = Logger{}
var _ kafkaGo.Logger = ErrorLogger{}
