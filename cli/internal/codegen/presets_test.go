package codegen

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
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
	if err := runPreset("webserver", dir, "", "", streams); err != nil {
		t.Fatalf("runPreset: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "appspec.json")); err != nil {
		t.Fatalf("appspec.json not written: %v", err)
	}
	// 已存在拒绝（声明是 user 文件，覆盖越权）。
	if err := runPreset("webserver", dir, "", "", streams); err == nil || !bytes.Contains([]byte(err.Error()), []byte("refusing to overwrite")) {
		t.Errorf("second run: err = %v, want refuse", err)
	}
	// 未知模板报错带候选清单。
	if err := runPreset("nope", dir, "", "", streams); err == nil || !bytes.Contains([]byte(err.Error()), []byte("webserver")) {
		t.Errorf("unknown preset: err = %v, want candidates", err)
	}
}

// TestRunPresetWithOverrides 是「一步起项目」的闭环：--name/--module 覆盖预设
// 占位符后**立即生成**，产物自洽（--check 零差异）。这是 G4 的验收判据——
// onexai 一条 create webserver 成型，bald 不该要求「落模板 → 改 JSON → 再生成」。
func TestRunPresetWithOverrides(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "blog-api")
	var out, errOut bytes.Buffer
	streams := ioStreams{Out: &out, ErrOut: &errOut}
	// 应用名取 "blog"：生成树按 <app>-<role> 组合，webserver 预设的角色是 api，
	// 于是落 cmd/blog-api/main.go——与 onexai 的 blog-apiserver 同形。
	if err := runPreset("webserver", dir, "blog", "github.com/you/blog-api", streams); err != nil {
		t.Fatalf("runPreset with overrides: %v\nstdout: %s\nstderr: %s", err, out.String(), errOut.String())
	}

	// 声明被覆盖：meta.name/module 是调用方给的值，不是预设占位符。
	as := &appspecv1.AppSpec{}
	raw, err := os.ReadFile(filepath.Join(dir, "appspec.json"))
	if err != nil {
		t.Fatalf("read appspec.json: %v", err)
	}
	if err := protojson.Unmarshal(raw, as); err != nil {
		t.Fatalf("parse appspec.json: %v", err)
	}
	if as.Meta.Name != "blog" || as.Meta.Module != "github.com/you/blog-api" {
		t.Fatalf("meta = %q/%q, want blog / github.com/you/blog-api", as.Meta.Name, as.Meta.Module)
	}
	// 其余形状原样：roles 与 bootstrap 段落被 protojson 回写后仍然完整。
	if len(as.Meta.Roles) == 0 || as.Bootstrap == nil {
		t.Fatalf("override lost declaration shape: roles=%d bootstrap=%v", len(as.Meta.Roles), as.Bootstrap)
	}

	// 目录树已经生成（一步到位），且与声明零差异。
	if _, err := os.Stat(filepath.Join(dir, "cmd", "blog-api", "main.go")); err != nil {
		t.Fatalf("tree not generated in one step: %v", err)
	}
	out.Reset()
	if err := runGenAppSpec(filepath.Join(dir, "appspec.json"), dir, false, true, streams); err != nil {
		t.Fatalf("generated tree drifts from declaration: %v", err)
	}
}
