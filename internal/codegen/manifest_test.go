package codegen

// manifest_test.go 覆盖 M4 / I8：生成确定性（双跑字节一致）与清单校验
// （--check 干净时零差异、有差异时失败并列出路径；--adopt 是唯一覆盖语义）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// snapshotTree 把 root 下的全部文件读成 相对路径 → 字节 的映射（用于双跑比对）。
func snapshotTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return out
}

// TestGen_Deterministic 双跑字节一致（含清单）：同一份声明两次生成到不同目录，
// 文件集合与每个文件的字节完全相同——I8「生成确定」。
func TestGen_Deterministic(t *testing.T) {
	dirA, dirB, specDir := t.TempDir(), t.TempDir(), t.TempDir()
	specPath := writeSpecJSON(t, specDir, guardFixture())

	for _, dir := range []string{dirA, dirB} {
		streams, _ := testStreams()
		if err := runGenAppSpec(specPath, dir, false, false, streams); err != nil {
			t.Fatalf("gen app --spec: %v", err)
		}
	}

	a, b := snapshotTree(t, dirA), snapshotTree(t, dirB)
	if len(a) != len(b) {
		t.Fatalf("dual run produced different file counts: %d vs %d", len(a), len(b))
	}
	for path, want := range a {
		got, ok := b[path]
		if !ok {
			t.Errorf("file %s missing from second run", path)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("file %s differs between runs (%d vs %d bytes)", path, len(want), len(got))
		}
	}
}

// TestGen_CheckClean 首次生成后 --check 干净；owned 文件被改动、清单缺失或
// 清单过期时 --check 失败并指出差异路径（I8「--check 零 diff」）。
func TestGen_CheckClean(t *testing.T) {
	dir := t.TempDir()
	specPath := writeSpecJSON(t, dir, guardFixture())

	streams, out := testStreams()
	if err := runGenAppSpec(specPath, dir, false, false, streams); err != nil {
		t.Fatalf("first gen: %v", err)
	}

	// 干净：--check 成功且不写盘。
	streams, out = testStreams()
	if err := runGenAppSpec(specPath, dir, false, true, streams); err != nil {
		t.Fatalf("clean tree must pass --check, got: %v", err)
	}
	if !strings.Contains(out.String(), "clean:") {
		t.Errorf("--check should report a clean tree, got: %q", out.String())
	}

	// owned 文件被改动 → out-of-date。
	manifestPath := filepath.Join(dir, appManifestName)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m genManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	owned := ""
	for _, f := range m.Files {
		if f.Ownership == "owned" {
			owned = f.Path
			break
		}
	}
	if owned == "" {
		t.Fatalf("manifest has no owned entries: %s", raw)
	}
	ownedPath := filepath.Join(dir, filepath.FromSlash(owned))
	orig, err := os.ReadFile(ownedPath)
	if err != nil {
		t.Fatalf("read owned file: %v", err)
	}
	if err := os.WriteFile(ownedPath, append(orig, []byte("\n// drift\n")...), 0o644); err != nil {
		t.Fatalf("mutate owned file: %v", err)
	}
	streams, _ = testStreams()
	err = runGenAppSpec(specPath, dir, false, true, streams)
	if err == nil || !strings.Contains(err.Error(), "out-of-date: "+owned) {
		t.Fatalf("mutated owned file must fail --check, got: %v", err)
	}
	if err := os.WriteFile(ownedPath, orig, 0o644); err != nil {
		t.Fatalf("restore owned file: %v", err)
	}

	// 清单缺失 → missing。
	if err := os.Remove(manifestPath); err != nil {
		t.Fatalf("remove manifest: %v", err)
	}
	streams, _ = testStreams()
	err = runGenAppSpec(specPath, dir, false, true, streams)
	if err == nil || !strings.Contains(err.Error(), "missing: "+appManifestName) {
		t.Fatalf("missing manifest must fail --check, got: %v", err)
	}

	// 清单过期（内容被改写）→ out-of-date。
	if err := os.WriteFile(manifestPath, append(raw, []byte(" ")...), 0o644); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}
	streams, _ = testStreams()
	err = runGenAppSpec(specPath, dir, false, true, streams)
	if err == nil || !strings.Contains(err.Error(), "out-of-date: "+appManifestName) {
		t.Fatalf("stale manifest must fail --check, got: %v", err)
	}

	// 清单恢复后重新干净。
	if err := os.WriteFile(manifestPath, raw, 0o644); err != nil {
		t.Fatalf("restore manifest: %v", err)
	}
	streams, _ = testStreams()
	if err := runGenAppSpec(specPath, dir, false, true, streams); err != nil {
		t.Fatalf("restored tree must pass --check, got: %v", err)
	}
}

// TestGen_AdoptOverwritesOwnedOnly --adopt 覆盖 owned 文件并重写清单，
// seeded 文件播种后归用户内容不被覆盖。
func TestGen_AdoptOverwritesOwnedOnly(t *testing.T) {
	dir := t.TempDir()
	specPath := writeSpecJSON(t, dir, guardFixture())

	streams, _ := testStreams()
	if err := runGenAppSpec(specPath, dir, false, false, streams); err != nil {
		t.Fatalf("first gen: %v", err)
	}

	ownedPath := filepath.Join(dir, "cmd", "demoapp", "main.go")
	if _, err := os.Stat(ownedPath); err != nil {
		t.Fatalf("expected owned file: %v", err)
	}
	if err := os.WriteFile(ownedPath, []byte("package main // hand edit\n"), 0o644); err != nil {
		t.Fatalf("mutate owned: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, appManifestName))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m genManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	seededPath := ""
	for _, f := range m.Files {
		if f.Ownership == "seeded" {
			seededPath = filepath.Join(dir, filepath.FromSlash(f.Path))
			break
		}
	}
	seededContent := []byte("package userowned // seeded\n")
	if seededPath != "" {
		if err := os.WriteFile(seededPath, seededContent, 0o644); err != nil {
			t.Fatalf("mutate seeded: %v", err)
		}
	}

	streams, _ = testStreams()
	if err := runGenAppSpec(specPath, dir, true, false, streams); err != nil {
		t.Fatalf("--adopt: %v", err)
	}

	got, err := os.ReadFile(ownedPath)
	if err != nil {
		t.Fatalf("read owned after adopt: %v", err)
	}
	if strings.Contains(string(got), "hand edit") {
		t.Errorf("--adopt must overwrite owned files")
	}
	if seededPath != "" {
		got, err := os.ReadFile(seededPath)
		if err != nil {
			t.Fatalf("read seeded after adopt: %v", err)
		}
		if string(got) != string(seededContent) {
			t.Errorf("--adopt must not overwrite seeded files")
		}
	}

	// adopt 后树重新干净。
	streams, _ = testStreams()
	if err := runGenAppSpec(specPath, dir, false, true, streams); err != nil {
		t.Fatalf("tree must be clean after --adopt, got: %v", err)
	}
}

// TestGenApp_ModeFlags --force 在 spec 模式退役（B5）；--adopt 与 --check 互斥；
// 模板模式不接受 --adopt/--check。
func TestGenApp_ModeFlags(t *testing.T) {
	if err := (&appOptions{Spec: "x.json", Force: true}).Validate(); err == nil || !strings.Contains(err.Error(), "--force is retired") {
		t.Fatalf("--force in spec mode must be rejected, got: %v", err)
	}
	if err := (&appOptions{Spec: "x.json", Adopt: true, Check: true}).Validate(); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("--adopt + --check must be rejected, got: %v", err)
	}
	if err := (&appOptions{Name: "demo", Adopt: true}).Validate(); err == nil || !strings.Contains(err.Error(), "template mode uses --force") {
		t.Fatalf("--adopt in template mode must be rejected, got: %v", err)
	}
	if err := (&domainOptions{Spec: "x.json", Adopt: true, Check: true}).Validate(); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("gen domain --adopt + --check must be rejected, got: %v", err)
	}
}

// TestGenDomain_CheckClean gen domain 首次落盘后 --check 干净；清单在
// 域生成物之外独立（domains.manifest.json），与 gen app 清单共存。
func TestGenDomain_CheckClean(t *testing.T) {
	dir, specPath := runGenAppInTemp(t, guardFixture())

	if _, err := os.Stat(filepath.Join(dir, domainManifestName)); err != nil {
		t.Fatalf("gen domain should write its manifest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, appManifestName)); err != nil {
		t.Fatalf("gen app manifest must survive gen domain: %v", err)
	}

	streams, out := testStreams()
	cmd := genDomainCmd(streams)
	cmd.SetArgs([]string{"--spec", specPath, "--check"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("gen domain --check on freshly generated tree: %v", err)
	}
	if !strings.Contains(out.String(), "clean:") {
		t.Errorf("gen domain --check should report clean, got: %q", out.String())
	}
}
