// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package selector_test

import (
	"context"
	"testing"

	"github.com/kalandramo/bald/selector"
)

func TestNewNodeAccessors(t *testing.T) {
	md := map[string]string{"zone": "a"}
	n := selector.NewNode("10.0.0.1:9000", "grpc", "svc", 42, md)

	if n.Address() != "10.0.0.1:9000" {
		t.Errorf("Address = %q", n.Address())
	}
	if n.Scheme() != "grpc" {
		t.Errorf("Scheme = %q", n.Scheme())
	}
	if n.ServiceName() != "svc" {
		t.Errorf("ServiceName = %q", n.ServiceName())
	}
	if n.Weight() != 42 {
		t.Errorf("Weight = %d", n.Weight())
	}
	if got := n.Metadata(); got["zone"] != "a" {
		t.Errorf("Metadata = %v", got)
	}
}

func TestRegistered(t *testing.T) {
	if !selector.Registered("round_robin") {
		t.Error("round_robin should be registered by test init")
	}
	if selector.Registered("no-such-strategy") {
		t.Error("unknown strategy must not report registered")
	}
}

func TestMustRegisterInvalidInput(t *testing.T) {
	mustPanic(t, "empty name", func() { selector.MustRegister("", selector.Factory(nil)) })
	mustPanic(t, "nil factory", func() { selector.MustRegister("nil-factory", nil) })
	mustPanic(t, "duplicate", func() { selector.MustRegister("round_robin", nil) })
}

func mustPanic(t *testing.T, name string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: expected panic", name)
		}
	}()
	f()
}

func TestNoopDone(t *testing.T) {
	selector.NoopDone(context.Background(), selector.DoneInfo{})
}
