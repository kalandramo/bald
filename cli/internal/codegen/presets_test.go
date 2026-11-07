package codegen

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestPresetsPassGates 是预置声明的验证闭环：每个预设写到临时目录后，必须
// 原样通过 gen app 的 Gate1/Gate2 与树构建（--check 零写入）。预设坏了 =
// 用户的第一条命令就红，比没有预设更糟——所以这里逐个实跑，不抽样。
// 新增预设只需加 presets/*.json，本测试自动覆盖。
func TestPresetsPassGates(t *testing.T) {
	names := PresetNames()
	if len(names) < 3 {
		t.Fatalf("expected at least 3 presets, got %v", names)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			raw, err := presetSpec(name)
			if err != nil {
				t.Fatalf("presetSpec: %v", err)
			}
			specPath := filepath.Join(dir, "appspec.json")
			if err := os.WriteFile(specPath, raw, 0o644); err != nil {
				t.Fatal(err)
			}
			var out, errOut bytes.Buffer
			streams := ioStreams{Out: &out, ErrOut: &errOut}
			// 两段式闭环：先生成（adopt 落树），再 --check 验零差异——证明预设
			// 过 Gate1/Gate2 且生成树自洽（声明的函数，双跑字节一致）。
			if err := runGenAppSpec(specPath, dir, true, false, streams); err != nil {
				t.Fatalf("preset %s fails gen app: %v\nstdout: %s\nstderr: %s", name, err, out.String(), errOut.String())
			}
			out.Reset()
			if err := runGenAppSpec(specPath, dir, false, true, streams); err != nil {
				t.Fatalf("preset %s fails gen app --check: %v\nstdout: %s\nstderr: %s", name, err, out.String(), errOut.String())
			}
		})
	}
}

// TestRunPreset 校验落盘语义：生成 appspec.json、已存在拒绝、未知模板报错带候选。
func TestRunPreset(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	streams := ioStreams{Out: &out, ErrOut: &errOut}
	if err := runPreset("webserver", dir, streams); err != nil {
		t.Fatalf("runPreset: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "appspec.json")); err != nil {
		t.Fatalf("appspec.json not written: %v", err)
	}
	// 已存在拒绝（声明是 user 文件，覆盖越权）。
	if err := runPreset("webserver", dir, streams); err == nil || !bytes.Contains([]byte(err.Error()), []byte("refusing to overwrite")) {
		t.Errorf("second run: err = %v, want refuse", err)
	}
	// 未知模板报错带候选清单。
	if err := runPreset("nope", dir, streams); err == nil || !bytes.Contains([]byte(err.Error()), []byte("webserver")) {
		t.Errorf("unknown preset: err = %v, want candidates", err)
	}
}
