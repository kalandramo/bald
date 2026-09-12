package http3

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"github.com/stretchr/testify/assert"
)

func HygrothermographHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("HygrothermographHandler [%s] [%s] [%s]\n", r.Proto, r.Method, r.RequestURI)

	if r.Method == "POST" {
		var in map[string]any
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fmt.Printf("decode error: %s\n", err.Error())
		}
		fmt.Printf("Payload: %v\n", in)
	}

	w.Header().Set("Content-Type", "application/json")
	var out = map[string]string{
		"Humidity":    strconv.FormatInt(int64(rand.Intn(100)), 10),
		"Temperature": strconv.FormatInt(int64(rand.Intn(100)), 10),
	}
	_ = json.NewEncoder(w).Encode(&out)
}

// TestServerAndClient 端到端：本地起 HTTP/3 server → QUIC 客户端 GET/POST。
// 原实现拆成 TestServer/TestClient 两个用例跨用例连接——server 随 TestServer
// 结束（defer cancel）已停，TestClient 必然 IdleTimeout，已合并为单用例内起停。
func TestServerAndClient(t *testing.T) {
	srv := NewServer(
		WithAddress("127.0.0.1:8800"),
	)

	srv.HandleFunc("/hygrothermograph", HygrothermographHandler)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := srv.Start(ctx); err != nil {
			t.Errorf("server start failed: %v", err)
		}
	}()

	defer func() {
		cancel()
		if err := srv.Stop(context.Background()); err != nil {
			t.Errorf("expected nil got %v", err)
		}
	}()

	transport := &http3.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		QUICConfig:      &quic.Config{},
	}
	cli := &http.Client{Transport: transport}
	defer transport.Close()

	// server 异步启动，QUIC 握手就绪轮询（最多 10s）
	var resp *http.Response
	var err error
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err = cli.Get("https://127.0.0.1:8800/hygrothermograph")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("http3 server not ready within 10s: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// GET
	{
		defer resp.Body.Close()
		var result map[string]string
		assert.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
		t.Logf("GET response: %v", result)
		assert.Equal(t, 2, len(result))
	}

	req := map[string]string{
		"Humidity":    strconv.FormatInt(int64(rand.Intn(100)), 10),
		"Temperature": strconv.FormatInt(int64(rand.Intn(100)), 10),
	}

	// POST
	body, _ := json.Marshal(req)
	resp, err = cli.Post("https://127.0.0.1:8800/hygrothermograph", "application/json", bytes.NewReader(body))
	assert.Nil(t, err)
	assert.NotNil(t, resp)
	if resp != nil {
		defer resp.Body.Close()
		var result map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&result)
		t.Logf("POST response: %v", result)
	}
}
