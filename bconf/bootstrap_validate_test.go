// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// bootstrap_validate_test.go 补齐 validate* 里「需特定非法配置才走到」的早退/报错
// 分支：nil 段放行、App 缺 name、slog rotate 各项负值、loki flush_interval 负值。
// 直接调用包内校验函数以精确命中单分支，happy path 与其余规则已由既有测试覆盖。

package bconf

import (
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

func TestValidateNilConfig(t *testing.T) {
	if err := Validate(nil); err == nil || !strings.Contains(err.Error(), "config is nil") {
		t.Errorf("Validate(nil) 应报 nil 配置错: %v", err)
	}
}

func TestValidateAppBranches(t *testing.T) {
	if err := validateApp(nil); err != nil {
		t.Errorf("validateApp(nil) 应放行: %v", err)
	}
	if err := validateApp(&bootstrapv1.App{Id: ""}); err == nil ||
		!strings.Contains(err.Error(), "id must not be empty") {
		t.Errorf("validateApp 空 id 应报错: %v", err)
	}
	// id 非空但 name 空 → 命中 name 分支。
	if err := validateApp(&bootstrapv1.App{Id: "svc"}); err == nil ||
		!strings.Contains(err.Error(), "name must not be empty") {
		t.Errorf("validateApp 空 name 应报错: %v", err)
	}
}

func TestValidateLoggerNil(t *testing.T) {
	if err := validateLogger(nil); err != nil {
		t.Errorf("validateLogger(nil) 应放行: %v", err)
	}
}

func TestValidateSlogRotateNegatives(t *testing.T) {
	cases := []struct {
		name string
		rot  *bootstrapv1.Logger_Slog_Rotate
		want string
	}{
		{"max_size", &bootstrapv1.Logger_Slog_Rotate{MaxSize: -1}, "max_size"},
		{"max_backups", &bootstrapv1.Logger_Slog_Rotate{MaxSize: 1, MaxBackups: -1}, "max_backups"},
		{"max_age", &bootstrapv1.Logger_Slog_Rotate{MaxSize: 1, MaxBackups: 1, MaxAge: -1}, "max_age"},
	}
	for _, tc := range cases {
		err := validateSlogSection(&bootstrapv1.Logger_Slog{Rotate: tc.rot}, "p")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("slog rotate %s 负值应报错，实得: %v", tc.want, err)
		}
	}
	// 三项皆非负 → 放行。
	if err := validateSlogSection(&bootstrapv1.Logger_Slog{
		Rotate: &bootstrapv1.Logger_Slog_Rotate{MaxSize: 1, MaxBackups: 1, MaxAge: 1},
	}, "p"); err != nil {
		t.Errorf("slog rotate 合法应放行: %v", err)
	}
}

func TestValidateLokiFlushIntervalNegative(t *testing.T) {
	err := validateLokiSection(&bootstrapv1.Logger_Loki{
		Endpoint:      "http://loki:3100",
		FlushInterval: -1,
	}, "p")
	if err == nil || !strings.Contains(err.Error(), "flush_interval") {
		t.Errorf("loki flush_interval 负值应报错，实得: %v", err)
	}
}
