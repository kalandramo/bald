package rocketmq

// fakenamesrv_test.go —— v2 包测试用的最小假名字服务。
//
// 只做一件事：把路由查询（code=105）答成「指向一台不回话的假 broker 的真路由」，
// 其余请求答成功。为什么要它：`PushConsumer.Subscribe` 之后 `Start()` 会立刻
// 取该主题的队列，取不到就报 `route info not found` 并让 Subscribe 失败——
// 于是「订阅者进不进注册表」这条判据根本没有可达路径。
//
// 订阅生命周期本身在进程内判（b.subscribers 与本包类型），不需要读线，所以这里
// 不记流水；需要读线的判据在 broker/rocketmq/contract 那族闸门里。
//
// 帧格式与 contract 包那份一致（[4B 帧长][4B 头长 + 高位 codec][JSON 头][body]），
// 两份各约百行且都是测试专用：contract 的测试包不可被本包 import，
// 把假线搬进生产目录（internal/fakers）又会让非测试代码依赖它，
// 所以这里选择重复——重复的部分只是协议外壳，判据本身没有重复。

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

// fakeCodeQueryRoute 是路由查询的请求码。本包只需要答这一种，其余请求一律答成功。
const fakeCodeQueryRoute = int16(105)

// startRouteFake 起一台假名字服务（路由答向一台不回话的假 broker），返回名字服务地址。
// 两个监听器都挂在 t.Cleanup 上。
func startRouteFake(t *testing.T) string {
	t.Helper()

	brokerAddr := startSilentEndpoint(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen namesrv: %v", err)
	}
	t.Cleanup(func() { _ = lis.Close() })

	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			go serveRouteFake(c, brokerAddr)
		}
	}()

	return lis.Addr().String()
}

// startSilentEndpoint 起一台只接受连接、读干字节、永不回话的端点，返回它的地址。
// 路由指过来的是它：客户端会真的拨号，但发送与心跳都只会超时，不影响本包判据。
func startSilentEndpoint(t *testing.T) string {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen broker: %v", err)
	}
	t.Cleanup(func() { _ = lis.Close() })

	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(io.Discard, c)
			}(c)
		}
	}()

	return lis.Addr().String()
}

func serveRouteFake(c net.Conn, brokerAddr string) {
	defer c.Close()

	for {
		_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))

		var size int32
		if err := binary.Read(c, binary.BigEndian, &size); err != nil {
			return
		}
		if size <= 0 || size > 1<<20 {
			return
		}
		frame := make([]byte, size)
		if _, err := io.ReadFull(c, frame); err != nil {
			return
		}

		hdrLen := int32(binary.BigEndian.Uint32(frame[:4]) & 0xFFFFFF)
		if 4+int(hdrLen) > len(frame) {
			return
		}

		var req struct {
			Code   int16 `json:"code"`
			Opaque int32 `json:"opaque"`
		}
		if err := json.Unmarshal(frame[4:4+hdrLen], &req); err != nil {
			return
		}

		var body []byte
		if req.Code == fakeCodeQueryRoute {
			body = []byte(fmt.Sprintf(`{"queueDatas":[{"brokerName":"fakebroker",`+
				`"readQueueNums":2,"writeQueueNums":2,"perm":6,"topicSynFlag":0}],`+
				`"brokerDatas":[{"brokerName":"fakebroker","cluster":"DefaultCluster",`+
				`"brokerAddrs":{"0":%q}}]}`, brokerAddr))
		}

		header, err := json.Marshal(map[string]any{
			"code":      int16(0), // ResSuccess
			"language":  "GO",
			"version":   317,
			"opaque":    req.Opaque,
			"flag":      1,
			"remark":    "fake: ok",
			"extFields": map[string]string{},
		})
		if err != nil {
			return
		}

		out := make([]byte, 8+len(header)+len(body))
		binary.BigEndian.PutUint32(out[0:4], uint32(4+len(header)+len(body)))
		binary.BigEndian.PutUint32(out[4:8], uint32(len(header)))
		copy(out[8:], header)
		copy(out[8+len(header):], body)

		_ = c.SetWriteDeadline(time.Now().Add(3 * time.Second))
		if _, err := c.Write(out); err != nil {
			return
		}
	}
}
