package codegen

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// DX-4：schema 是 proto 描述符的只读投影。测试钉住三件事——
// 顶层键与 json_name 一致、嵌套 message 递归展开、输出字节确定（无 map 随机序）。
func TestGenSchema_DerivesFromDescriptor(t *testing.T) {
	var out, errOut bytes.Buffer
	o := &schemaOptions{Indent: true, ioStreams: ioStreams{Out: &out, ErrOut: &errOut}}
	if err := o.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if errOut.Len() > 0 {
		t.Errorf("unexpected stderr: %s", errOut.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("schema is not valid JSON: %v\n%s", err, out.String())
	}
	if doc["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Errorf("$schema = %v", doc["$schema"])
	}
	props, ok := doc["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties missing: %v", doc)
	}
	for _, key := range []string{"meta", "components", "bootstrap", "domains", "variants"} {
		if _, ok := props[key]; !ok {
			t.Errorf("top-level field %q missing from schema", key)
		}
	}
	meta, ok := props["meta"].(map[string]any)
	if !ok {
		t.Fatalf("meta is not an object schema: %v", props["meta"])
	}
	metaProps, ok := meta["properties"].(map[string]any)
	if !ok || metaProps["name"] == nil || metaProps["module"] == nil {
		t.Errorf("meta.name/meta.module not derived: %v", meta)
	}
	if doc["additionalProperties"] != false {
		t.Errorf("additionalProperties must be false (protojson rejects unknown fields)")
	}
}

func TestGenSchema_Deterministic(t *testing.T) {
	run := func() string {
		var out, errOut bytes.Buffer
		o := &schemaOptions{Indent: false, ioStreams: ioStreams{Out: &out, ErrOut: &errOut}}
		if err := o.run(); err != nil {
			t.Fatalf("run: %v", err)
		}
		return out.String()
	}
	first := run()
	for i := 0; i < 5; i++ {
		if got := run(); got != first {
			t.Fatalf("schema output is not deterministic (run %d)", i)
		}
	}
	if !strings.Contains(first, `"type":"object"`) {
		t.Errorf("schema has no object type: %s", first[:min(len(first), 200)])
	}
}
