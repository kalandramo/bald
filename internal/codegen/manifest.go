package codegen

// manifest.go 是生成清单与「生成物是否干净」的判定（M4，I8）。
//
// 清单记录一次生成的三件事：声明文件的内容摘要、每个非用户文件（owned/
// seeded）的归属与落盘字节摘要、以及格式版本。它把「生成物最新」从不可判定
// 变成可判定：
//   - --check：按当前声明在内存里重算整棵树，与磁盘逐字节比对（零写入），
//     任何 owned 差异 / 缺失 / 清单缺失或过期都判为不干净并列出差异路径；
//   - --adopt：覆盖 owned 文件（seeded 文件播种后归用户，永不覆盖）并重写
//     清单，是旧 --force 的替代语义（B5）。
//
// 清单本身不进入 Files（否则自指）：--check 以「重新渲染的清单字节」为期望值
// 单独比对。

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// manifestVersion 是清单格式版本；格式变化时递增，旧清单据此判为过期。
const manifestVersion = 1

// 两个生成器各持一份清单：gen app 覆盖整个目录树，gen domain 只覆盖域
// 相关生成物；同根目录下共存时不互相覆盖。
const (
	appManifestName    = "appspec.manifest.json"
	domainManifestName = "domains.manifest.json"
)

// genManifest 是一次生成的记录。
type genManifest struct {
	Version int             `json:"version"`
	Tool    string          `json:"tool"`
	Spec    string          `json:"spec"` // 声明文件（AppSpec JSON）内容的 sha256
	Files   []manifestEntry `json:"files"`
}

// manifestEntry 是单条生成物记录。Path 恒为斜杠形态，跨平台可比。
type manifestEntry struct {
	Path      string `json:"path"`
	Ownership string `json:"ownership"` // owned | seeded
	Hash      string `json:"hash"`      // 落盘字节（.go 已 gofmt）的 sha256
}

// prepareContent 返回生成物的最终落盘字节：.go 先 gofmt，其余原样。
// 生成与校验共用这一通道，保证清单摘要与磁盘字节同源。
func prepareContent(f genFile) ([]byte, error) {
	if !f.GoSrc {
		return f.Raw, nil
	}
	formatted, err := format.Source(f.Raw)
	if err != nil {
		return nil, fmt.Errorf("gofmt %s: %w", f.Path, err)
	}
	return formatted, nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// buildManifest 依据生成计划构建清单与落盘字节（不含清单自身，见包注释）。
func buildManifest(tool, specPath string, files []genFile) (*genManifest, []byte, error) {
	spec, err := os.ReadFile(specPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read spec: %w", err)
	}
	m := &genManifest{Version: manifestVersion, Tool: tool, Spec: sha256Hex(spec)}
	for _, f := range files {
		if f.Own == User {
			continue // 用户文件不属于生成物
		}
		entry := manifestEntry{Path: filepath.ToSlash(f.Path), Ownership: "owned"}
		if f.Own == Seeded {
			// seeded 播种后归用户，内容差异是合法的——摘要恒为标记值，
			// 用户修改骨架不会让清单过期（--check 只判存在性）。
			entry.Ownership, entry.Hash = "seeded", "seeded"
			m.Files = append(m.Files, entry)
			continue
		}
		content, err := prepareContent(f)
		if err != nil {
			return nil, nil, err
		}
		entry.Hash = sha256Hex(content)
		m.Files = append(m.Files, entry)
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal manifest: %w", err)
	}
	return m, append(raw, '\n'), nil
}

// checkTree 校验磁盘生成物与「按当前声明重新生成」的结果一致（I8）：
// 零写入，差异按生成计划顺序收集为一条错误（列出路径与类别）。
// seeded 文件只判存在性——播种后归用户，内容差异是合法的。
func checkTree(root, manifestName string, files []genFile, wantRaw []byte, streams ioStreams) error {
	var diffs []string
	for _, f := range files {
		if f.Own == User {
			continue
		}
		path := filepath.Join(root, f.Path)
		got, err := os.ReadFile(path)
		switch {
		case os.IsNotExist(err):
			diffs = append(diffs, "missing: "+f.Path)
			continue
		case err != nil:
			return fmt.Errorf("read %s: %w", path, err)
		}
		if f.Own == Seeded {
			continue
		}
		want, err := prepareContent(f)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			diffs = append(diffs, "out-of-date: "+f.Path)
		}
	}
	manifestPath := filepath.Join(root, manifestName)
	gotManifest, err := os.ReadFile(manifestPath)
	switch {
	case os.IsNotExist(err):
		diffs = append(diffs, "missing: "+manifestName)
	case err != nil:
		return fmt.Errorf("read %s: %w", manifestPath, err)
	case !bytes.Equal(gotManifest, wantRaw):
		diffs = append(diffs, "out-of-date: "+manifestName)
	}
	if len(diffs) > 0 {
		return fmt.Errorf("generated tree is not clean (%d difference(s)); rerun with --adopt:\n  %s",
			len(diffs), strings.Join(diffs, "\n  "))
	}
	fmt.Fprintln(streams.Out, "clean:", len(files), "files in sync with spec")
	return nil
}

// writeManifest 把清单落盘。清单是派生元数据（每次生成必然变化），
// 恒覆盖写，不参与 owned 的「默认拒绝」守卫。
func writeManifest(root, name string, raw []byte, streams ioStreams) error {
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	fmt.Fprintln(streams.Out, "manifest:", path)
	return nil
}
