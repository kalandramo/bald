// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// util_batch_test.go 覆盖 bconf 标量/地址/duration 解析助手的全部分支——它们被
// flag 写入器与 config 装载共用，是纯函数，直接逐分支断言最省。

package bconf

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
)

func TestValidateAddress(t *testing.T) {
	ok := []string{":8080", "127.0.0.1:8080", "[::1]:9000", ":0", "0.0.0.0:1"}
	for _, a := range ok {
		if err := validateAddress(a); err != nil {
			t.Errorf("validateAddress(%q) = %v, want nil", a, err)
		}
	}
	bad := []string{"", "no-port", "not-an-ip:80", ":99999", ":abc", ":0x10", "1.2.3.4"}
	for _, a := range bad {
		if err := validateAddress(a); err == nil {
			t.Errorf("validateAddress(%q) = nil, want error", a)
		}
	}
}

func TestDurationToStringAndParse(t *testing.T) {
	if durationToString(nil) != "0s" {
		t.Errorf("durationToString(nil) = %q, want 0s", durationToString(nil))
	}
	if got := durationToString(durationpb.New(90 * time.Second)); got != "1m30s" {
		t.Errorf("durationToString(90s) = %q, want 1m30s", got)
	}

	// parseDuration：空串→零时长；带空格→trim 解析；非法→错误。
	if d, err := parseDuration(""); err != nil || d.AsDuration() != 0 {
		t.Errorf(`parseDuration("") = (%v, %v)`, d, err)
	}
	if d, err := parseDuration("  10s  "); err != nil || d.AsDuration() != 10*time.Second {
		t.Errorf(`parseDuration(" 10s ") = (%v, %v)`, d, err)
	}
	if _, err := parseDuration("nope"); err == nil {
		t.Error(`parseDuration("nope") 应报错`)
	}
}

func TestParseBoolBranches(t *testing.T) {
	for _, s := range []string{"1", "t", "true", "TRUE", "yes", "y", "on"} {
		if v, err := parseBool(s); err != nil || !v {
			t.Errorf("parseBool(%q) = (%v, %v), want true", s, v, err)
		}
	}
	for _, s := range []string{"0", "f", "false", "no", "n", "off", ""} {
		if v, err := parseBool(s); err != nil || v {
			t.Errorf("parseBool(%q) = (%v, %v), want false", s, v, err)
		}
	}
	// 落到 strconv.ParseBool 兜底：合法 "True" 仍解析，非法报错。
	if v, err := parseBool("True"); err != nil || !v {
		t.Errorf(`parseBool("True") = (%v, %v)`, v, err)
	}
	if _, err := parseBool("maybe"); err == nil {
		t.Error(`parseBool("maybe") 应报错`)
	}
}

func TestParseNumbers(t *testing.T) {
	if n, err := parseInt32(" 42 "); err != nil || n != 42 {
		t.Errorf("parseInt32 = (%d,%v)", n, err)
	}
	if _, err := parseInt32("x"); err == nil {
		t.Error("parseInt32 bad should err")
	}
	if n, err := parseInt64("-7"); err != nil || n != -7 {
		t.Errorf("parseInt64 = (%d,%v)", n, err)
	}
	if _, err := parseInt64("y"); err == nil {
		t.Error("parseInt64 bad should err")
	}
	if n, err := parseUint32("3"); err != nil || n != 3 {
		t.Errorf("parseUint32 = (%d,%v)", n, err)
	}
	if _, err := parseUint32("z"); err == nil {
		t.Error("parseUint32 bad should err")
	}
	if n, err := parseUint64("9"); err != nil || n != 9 {
		t.Errorf("parseUint64 = (%d,%v)", n, err)
	}
	if _, err := parseUint64("w"); err == nil {
		t.Error("parseUint64 bad should err")
	}
	if f, err := parseFloat32("1.5"); err != nil || f != 1.5 {
		t.Errorf("parseFloat32 = (%v,%v)", f, err)
	}
	if _, err := parseFloat32("q"); err == nil {
		t.Error("parseFloat32 bad should err")
	}
	if f, err := parseFloat64("2.5"); err != nil || f != 2.5 {
		t.Errorf("parseFloat64 = (%v,%v)", f, err)
	}
	if _, err := parseFloat64("e"); err == nil {
		t.Error("parseFloat64 bad should err")
	}
}

func TestHostname(t *testing.T) {
	// 主机名获取失败返回 "unknown"，成功返回非空——两者都是可接受读数。
	if h := hostname(); h == "" {
		t.Error("hostname 不应为空串（失败路径也返回 unknown）")
	}
}
