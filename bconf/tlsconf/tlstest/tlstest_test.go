// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package tlstest

import (
	"encoding/pem"
	"testing"
)

// TestGenerateAndTLS 走通 happy path：现签一套证书材料，字段非空且 PEM 可解，
// ServerTLS/ClientTLS 返回内联 PEM 的契约结构。
func TestGenerateAndTLS(t *testing.T) {
	c, err := Generate()
	if err != nil {
		t.Fatalf("Generate 出错: %v", err)
	}
	for name, b := range map[string][]byte{
		"CA": c.CA, "ServerCert": c.ServerCert, "ServerKey": c.ServerKey,
		"ClientCert": c.ClientCert, "ClientKey": c.ClientKey,
	} {
		if len(b) == 0 {
			t.Fatalf("%s 为空", name)
		}
		if blk, _ := pem.Decode(b); blk == nil {
			t.Fatalf("%s 不是合法 PEM", name)
		}
	}
	st := c.ServerTLS()
	if st.GetConfig().GetCertPem() == nil || st.GetConfig().GetCaPem() == nil {
		t.Fatal("ServerTLS 契约字段缺失")
	}
	ct := c.ClientTLS()
	if ct.GetConfig().GetCertPem() == nil || ct.GetConfig().GetCaPem() == nil {
		t.Fatal("ClientTLS 契约字段缺失")
	}
}
