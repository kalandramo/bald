package cobramcp

import (
	"testing"

	baldlog "github.com/kalandramo/bald/log"
)

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected baldlog.Level
	}{
		{"debug", baldlog.LevelDebug},
		{"DEBUG", baldlog.LevelDebug},
		{"info", baldlog.LevelInfo},
		{"INFO", baldlog.LevelInfo},
		{"warn", baldlog.LevelWarn},
		{"WARN", baldlog.LevelWarn},
		{"error", baldlog.LevelError},
		{"ERROR", baldlog.LevelError},
		{"unknown", baldlog.LevelInfo}, // Default
		{"", baldlog.LevelInfo},        // Default
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := parseLogLevel(tt.input)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}
