//go:build !grpcgw

// 默认构建（gin 面）的 REST 回归：gen domain 生成的 BindGreet 是旗舰默认
// 路径，-tags grpcgw 的转码面测试覆盖不到它，故用 !grpcgw 限定。
package bootstrap

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGreet_REST_GeneratedBinding(t *testing.T) {
	_, restAddr, stop := startApp(t)
	defer stop()

	base := "http://" + restAddr
	client := &http.Client{}

	post := func(t *testing.T, body string) (int, string) {
		t.Helper()
		resp, err := client.Post(base+"/v1/greet", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST /v1/greet: %v", err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}

	// body "*" 的 POST 入口。
	status, body := post(t, `{"name":"world"}`)
	if status != http.StatusOK || !strings.Contains(body, "hello, world") {
		t.Errorf("POST /v1/greet = %d %s, want 200 with greet", status, body)
	}

	// 同一份 proto 契约的路径变量入口。
	resp, err := client.Get(base + "/v1/greet/bald")
	if err != nil {
		t.Fatalf("GET /v1/greet/bald: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "hello, bald") {
		t.Errorf("GET /v1/greet/bald = %d %s, want 200 with greet", resp.StatusCode, raw)
	}

	// proto 注解规则（buf.validate）在 gin 绑定下同样生效。
	status, body = post(t, `{"name":""}`)
	if status != http.StatusBadRequest || !strings.Contains(body, "at least 1 characters") {
		t.Errorf("POST invalid = %d %s, want 400 with annotation rule message", status, body)
	}

	// 手写 Validator 规则同样生效（绑定与 gRPC 共用域校验契约）。
	status, body = post(t, `{"name":"root"}`)
	if status != http.StatusBadRequest || !strings.Contains(body, "为系统保留字") {
		t.Errorf("POST reserved = %d %s, want 400 with RESERVED_NAME message", status, body)
	}
}
