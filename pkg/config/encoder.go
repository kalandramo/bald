package config

import (
	"bytes"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// encoder 将 map 序列化为指定格式。
type encoder struct {
	format string
	buf    *bytes.Buffer
}

func newEncoder(format string, buf *bytes.Buffer) *encoder {
	return &encoder{format: format, buf: buf}
}

func (e *encoder) Encode(v any) error {
	switch e.format {
	case "json":
		enc := json.NewEncoder(e.buf)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	case "yaml", "yml":
		b, err := yaml.Marshal(v)
		if err != nil {
			return err
		}
		_, err = e.buf.Write(b)
		return err
	default:
		return fmt.Errorf("unsupported format %q", e.format)
	}
}
