package tcp

// byte_order_wire_test.go —— 字节序实例化的线上判据：
//   1. 客户端与服务端各用 WithClientByteOrder / WithByteOrder 指定大端，
//      整包往返（客户端发 → 服务端解 → 服务端回 → 客户端解）正常；
//   2. 裸 socket 上类型头确实是配置的字节序（读回 4 字节按大端解出类型）；
//   3. 过程不改进程级默认（defaultByteOrder 仍为小端）。
//
// 判据的区分力：把任一端改回小端，类型头解出 0x01000000 / 0x02000000 这类
// 不存在的类型，handler 不触发、往返用例超时（见登记册里的变异记录）。

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"github.com/kalandramo/bald/encoding"
	jsoncodec "github.com/kalandramo/bald/encoding/json"
)

const (
	byteOrderProbeRequest  = 1
	byteOrderProbeResponse = 2
)

type byteOrderProbe struct {
	Value string `json:"value"`
}

// startByteOrderServer 起一个 127.0.0.1:0 的大端回显服务器：
// 收到类型 1 的探针后按同一会话回类型 2（载荷原样）。
func startByteOrderServer(t *testing.T) (*Server, string) {
	t.Helper()
	if encoding.GetCodec("json") == nil {
		encoding.MustRegister(jsoncodec.New())
	}
	srv := NewServer(
		WithAddress("127.0.0.1:0"),
		WithByteOrder(binary.BigEndian),
		WithCodec("json"),
	)
	RegisterServerMessageHandler(srv, byteOrderProbeRequest, func(sid SessionID, msg *byteOrderProbe) error {
		return srv.SendMessage(sid, byteOrderProbeResponse, msg)
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = srv.Stop(context.Background())
	})
	go func() { _ = srv.Start(ctx) }()
	return srv, waitEndpoint(t, srv)
}

// 两端都按实例配置大端：整包往返成功。
func TestByteOrder_PerInstanceRoundTrip(t *testing.T) {
	_, addr := startByteOrderServer(t)
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split endpoint %q: %v", addr, err)
	}

	// 客户端把 endpoint 当 URL 解析：带点的主机（IP）会让 url.Parse 报错并留下
	// nil endpoint；hostname 形态解析成功，endpoint.String() 仍是 host:port。
	cli := NewClient(
		WithEndpoint("localhost:"+port),
		WithClientCodec("json"),
		WithClientByteOrder(binary.BigEndian),
	)
	t.Cleanup(cli.Disconnect)

	echoed := make(chan string, 1)
	RegisterClientMessageHandler(cli, byteOrderProbeResponse, func(msg *byteOrderProbe) error {
		echoed <- msg.Value
		return nil
	})
	if err := cli.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := cli.SendMessage(byteOrderProbeRequest, &byteOrderProbe{Value: "big-endian"}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	select {
	case got := <-echoed:
		if got != "big-endian" {
			t.Fatalf("echo = %q, want big-endian", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no echo: 大端实例头没有被对端按大端解出（handler 未触发）")
	}

	if defaultByteOrder != binary.LittleEndian {
		t.Fatalf("per-instance byte order mutated process default: %v", defaultByteOrder)
	}
}

// 裸 socket：类型头字节序就是配置的大端（服务端解与回都按同一序）。
func TestByteOrder_WireHeader(t *testing.T) {
	_, addr := startByteOrderServer(t)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	payload, err := json.Marshal(byteOrderProbe{Value: "raw"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame[:4], byteOrderProbeRequest)
	copy(frame[4:], payload)
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write frame: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		t.Fatalf("read response header: %v (服务端未按大端解出请求)", err)
	}
	if got := binary.BigEndian.Uint32(header); got != byteOrderProbeResponse {
		t.Fatalf("response type header = %#x (big-endian), want %#x", got, byteOrderProbeResponse)
	}
}
