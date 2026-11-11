// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package convention

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedPRDTree 在隔离项目根里落 PRD 工作流产物（prompt 与翻译产物的测试夹具）。
func seedPRDTree(t *testing.T, promptContent, prdContent string) string {
	t.Helper()
	dir := t.TempDir()
	if promptContent != "" {
		if err := os.MkdirAll(filepath.Join(dir, prdDir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, prdDir, "ticket.prompt.md"), []byte(promptContent), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if prdContent != "" {
		if err := os.MkdirAll(filepath.Join(dir, prdDir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, prdDir, "ticket.prd.md"), []byte(prdContent), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const validPrompt = `请搭建一个工单管理系统：包含用户认证、工单 CRUD、管理 CLI。先查规范。
`

const validPRD = `# PRD: 工单管理系统

## Introduction
工单跟踪与闭环。

### US-001: 用户认证
**Status:** todo
**Priority:** 1
**Description:** As a 系统用户, I want 注册登录 so that 受保护资源可访问。

**Acceptance Criteria:**
- [ ] 登录返回鉴权凭据
`

func TestCheckPRDPromptSeam(t *testing.T) {
	t.Run("seam present → no finding", func(t *testing.T) {
		dir := seedPRDTree(t, validPrompt, "")
		findings, skip, err := checkPRDPromptSeam(dir)
		if err != nil || skip != "" || len(findings) != 0 {
			t.Fatalf("findings=%v skip=%q err=%v", findings, skip, err)
		}
	})
	t.Run("seam missing → finding bound to prd-r06", func(t *testing.T) {
		dir := seedPRDTree(t, "请搭建一个系统。", "")
		findings, _, err := checkPRDPromptSeam(dir)
		if err != nil || len(findings) != 1 {
			t.Fatalf("findings=%v err=%v, want 1 finding", findings, err)
		}
		if findings[0].RuleID != "prd-r06" {
			t.Errorf("rule = %s, want prd-r06", findings[0].RuleID)
		}
	})
	t.Run("no prd dir → skip", func(t *testing.T) {
		dir := seedPRDTree(t, "", "")
		_, skip, err := checkPRDPromptSeam(dir)
		if err != nil || skip == "" {
			t.Fatalf("skip=%q err=%v, want skip", skip, err)
		}
	})
	t.Run("dir is a file → skip (ENOTDIR)", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "afile")
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, skip, err := checkPRDPromptSeam(dir)
		if err != nil || skip == "" {
			t.Fatalf("skip=%q err=%v, want skip", skip, err)
		}
	})
}

func TestCheckPRDStructure(t *testing.T) {
	t.Run("four fields present → no finding", func(t *testing.T) {
		dir := seedPRDTree(t, "", validPRD)
		findings, _, err := checkPRDStructure(dir)
		if err != nil || len(findings) != 0 {
			t.Fatalf("findings=%v err=%v", findings, err)
		}
	})
	t.Run("missing fields → finding bound to prd-r03", func(t *testing.T) {
		broken := strings.Replace(validPRD, "**Acceptance Criteria:**", "", 1)
		broken = strings.Replace(broken, "**Status:**", "", 1)
		dir := seedPRDTree(t, "", broken)
		findings, _, err := checkPRDStructure(dir)
		if err != nil || len(findings) != 1 {
			t.Fatalf("findings=%v err=%v, want 1 finding", findings, err)
		}
		if findings[0].RuleID != "prd-r03" {
			t.Errorf("rule = %s, want prd-r03", findings[0].RuleID)
		}
		if !strings.Contains(findings[0].Message, "**Status:**") || !strings.Contains(findings[0].Message, "**Acceptance Criteria:**") {
			t.Errorf("message 应列出缺失字段，got %s", findings[0].Message)
		}
	})
	t.Run("no prd dir → skip", func(t *testing.T) {
		dir := seedPRDTree(t, "", "")
		_, skip, err := checkPRDStructure(dir)
		if err != nil || skip == "" {
			t.Fatalf("skip=%q err=%v, want skip", skip, err)
		}
	})
}

// TestPRDCardsThroughRunCheck 经 runCheck 全链路验证：prd 卡声明的两个 checker
// 在夹具树上被调度、finding 的 rule 绑定正确（卡 → checks → 注册表的完整通路）。
func TestPRDCardsThroughRunCheck(t *testing.T) {
	dir := seedPRDTree(t, "请搭建一个系统。", validPRD) // prompt 缺接缝、结构完整
	res, err := runCheck(checkOptions{Dir: dir, MinLevel: "mandatory"})
	if err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	foundRules := map[string]bool{}
	for _, f := range res.Found {
		foundRules[f.RuleID] = true
	}
	if !foundRules["prd-r06"] {
		t.Errorf("prompt 缺接缝应产出 prd-r06 finding，got %v", res.Found)
	}
	if foundRules["prd-r03"] {
		t.Error("结构完整不应产出 prd-r03 finding")
	}
}

// TestCheckLoggingStdlib 覆盖 logging-stdlib checker 的三个分支：stdlib log
// 直用 → finding（绑 logging-r01）；无 go.mod → skip；测试文件与 bald/log
// 引用不误报。
func TestCheckLoggingStdlib(t *testing.T) {
	t.Run("stdlib log import → finding", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module demo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		src := "package main\n\nimport (\n\t\"log\"\n)\n\nfunc main() { log.Println(\"hi\") }\n"
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		findings, skip, err := checkLoggingStdlib(dir)
		if err != nil || skip != "" || len(findings) != 1 {
			t.Fatalf("findings=%v skip=%q err=%v", findings, skip, err)
		}
		if findings[0].RuleID != "logging-r01" {
			t.Errorf("rule = %s, want logging-r01", findings[0].RuleID)
		}
	})
	t.Run("bald/log and test files → no finding", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module demo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		ok := "package main\n\nimport \"github.com/kalandramo/bald/log\"\n"
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(ok), 0o644); err != nil {
			t.Fatal(err)
		}
		tst := "package main\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) { _ = t }\n"
		if err := os.WriteFile(filepath.Join(dir, "main_test.go"), []byte(tst), 0o644); err != nil {
			t.Fatal(err)
		}
		findings, _, err := checkLoggingStdlib(dir)
		if err != nil || len(findings) != 0 {
			t.Fatalf("findings=%v err=%v, want clean", findings, err)
		}
	})
	t.Run("no go.mod → skip", func(t *testing.T) {
		dir := t.TempDir()
		_, skip, err := checkLoggingStdlib(dir)
		if err != nil || skip == "" {
			t.Fatalf("skip=%q err=%v, want skip", skip, err)
		}
	})
}
