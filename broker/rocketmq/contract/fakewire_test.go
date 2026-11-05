package contract

// fakewire_test.go —— §7.24 离线闸门的观测面：进程内的假 RocketMQ 端点。
//
// 这一族闸门（etcd §7.14 / consul §7.18 / asynq §7.19 / gorm §7.20 / nacos §7.22 /
// mongodb §7.23）都要先回答同一个前提问题：**不靠真后端能不能读出这一行**。
// rocketmq 的答案是「能，八行全能」，因为 v2 客户端把每一次请求都以**明文 JSON 帧**
// 发到本机监听端口上（`JsonCodecs`，帧头 `codecType` 高字节为 0）：
//
//	[4B frame_size][4B header_len + 高字节 codec][JSON header][body]
//	header: {code, language, version, opaque, flag, remark, extFields}
//
// 于是「契约某行有没有落到 SDK」= 「线上某个请求的某个字段是不是那个值」：
// 路由查询 code=105 的 extFields.topic、心跳 code=34 的 body.clientID、
// 发送 code=10 的 extFields.producerGroup、ACL 签名 code 无关地挂在 extFields 上。
// 本文件做四件事：收帧并解码、按 code 回一个最小可用应答、把请求记下来供断言、
// 记连接的开/关（停机腿除了读流水，还要读这两个数，见 teardown_offline_test.go）。
//
// 应答的最小集（本机实测，v2.1.2）：
//   - 105 且 routeTo 为空 ⇒ code=17（topic not exist）：生产者每次 SendSync 迭代都会
//     重查路由，于是 retry_count 变成**可直接数的请求个数**，且整轮只花几毫秒；
//   - 105 且 routeTo 非空 ⇒ 回真路由（queueDatas + brokerDatas.brokerAddrs["0"]），
//     客户端随即**连上那台假 broker** 去发送/心跳，生产与订阅两条路都打得通；
//   - 其余 code ⇒ code=0 成功。
//
// 假 broker 用 silent（收帧不回话）：发送超时由 SDK 自己的 3s 兜住，本族断言只看
// 「请求到了没有、字段对不对」，不看应答，所以不必模拟 broker 的语义。
//
// TestMain 注册 json 编解码器：`broker.Marshal` 在 codec 为 nil 时报错，
// Publish 连帧都发不出去（与 kafka/etcd 的 contract 测试同形）。

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kalandramo/bald/encoding"
	jsoncodec "github.com/kalandramo/bald/encoding/json"
)

func TestMain(m *testing.M) {
	if encoding.GetCodec("json") == nil {
		encoding.MustRegister(jsoncodec.New())
	}
	os.Exit(m.Run())
}

// 线上请求码（v2.1.2 的 internal/remote 常量表里对应的三个）。
const (
	codeSend              = int16(10)
	codeHeartbeat         = int16(34)
	codeQueryRoute        = int16(105)
	codeTopicNotExistResp = int16(17)
)

// wireReq 是一次已解码的请求。at 用来量「心跳在第几毫秒到达」，kind 只是给失败信息打标。
// 头部的 language/version/flag/remark 不解码：客户端不读应答里的这些字段，
// 响应侧又是现搭的 map，没有必要与本结构体共用一份形状。
type wireReq struct {
	At   time.Time `json:"-"`
	Kind string    `json:"-"`

	Code   int16             `json:"code"`
	Opaque int32             `json:"opaque"`
	Ext    map[string]string `json:"extFields"`
	Body   string            `json:"-"`
}

// ext 安全取值：请求没带某个 extField 时返回 ""，断言里不必先判 nil。
func (r wireReq) ext(key string) string { return r.Ext[key] }

// topicQuery 判「这是不是查 topic 的路由请求」，并给出被查的 topic。
func (r wireReq) topicQuery() (string, bool) {
	if r.Code != codeQueryRoute {
		return "", false
	}
	return r.Ext["topic"], true
}

type fakeEnd struct {
	kind    string
	lis     net.Listener
	routeTo string // 空 ⇒ 每个路由查询答 17；非空 ⇒ 回指向该地址的路由
	silent  bool   // true ⇒ 只记账不回话

	mu   sync.Mutex
	reqs []wireReq

	// open/closes 记「当前挂着的连接数」与「累计关掉的连接数」。本机实测（v2.1.2）：
	// consumer 停机后**假 broker 的连接当场归零**（remoteClient.ShutDown 关掉自己的连接池），
	// 但名字服务上会留一条已空闲、再也无人发请求的连接——所以「连接归零」只适合当 broker 侧
	// 的辅助读数；「后台线程退了」的主判据是流水冻结（teardown_offline_test.go 的 assertFlowFrozen）。
	open   int
	closes int
}

// newFakeNameServer 起一台会答路由的假名字服务。routeTo 为空时所有主题都「不存在」。
func newFakeNameServer(t *testing.T, kind, routeTo string) *fakeEnd {
	t.Helper()
	return newFakeEnd(t, &fakeEnd{kind: kind, routeTo: routeTo})
}

// newFakeBroker 起一台假 broker：接受连接、记录请求、永不回话。
// 它只用来当「路由应答里那个地址」的收件人，所以 silent 是本质而不是省事。
func newFakeBroker(t *testing.T, kind string) *fakeEnd {
	t.Helper()
	return newFakeEnd(t, &fakeEnd{kind: kind, silent: true})
}

func newFakeEnd(t *testing.T, f *fakeEnd) *fakeEnd {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f.lis = lis
	// 先关监听再退出读循环：反过来的话在途连接的错误会晚于 t.Cleanup 返回，-race 下是噪声。
	t.Cleanup(func() { _ = lis.Close() })
	go f.serve()
	return f
}

func (f *fakeEnd) addr() string { return f.lis.Addr().String() }

func (f *fakeEnd) seen() []wireReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]wireReq, len(f.reqs))
	copy(out, f.reqs)
	return out
}

func (f *fakeEnd) record(r wireReq) {
	f.mu.Lock()
	f.reqs = append(f.reqs, r)
	f.mu.Unlock()
}

// openConns 返回当前仍挂着的连接数；closedConns 返回累计已关掉的连接数（见 fakeEnd 的注释）。
func (f *fakeEnd) openConns() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.open
}

func (f *fakeEnd) closedConns() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes
}

// countTopicQuery 返回「查 topic 这个主题的路由请求」条数。retry_count 一条腿就是靠它
// 数出来的，所以这里不做任何模糊匹配。
func (f *fakeEnd) countTopicQuery(topic string) int {
	var n int
	for _, r := range f.seen() {
		if got, ok := r.topicQuery(); ok && got == topic {
			n++
		}
	}
	return n
}

// eventually 轮询到 cond 满足为止；超时就把整份请求流水打进失败信息里。
// 用轮询而不是固定 sleep：固定值在本族里会把「心跳第 1.0s 到达」写成「睡 2.6s」，
// 慢机器上照样翻车，快机器上白等。
func (f *fakeEnd) eventually(t *testing.T, timeout time.Duration, want string, cond func([]wireReq) bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	var reqs []wireReq
	for {
		reqs = f.seen()
		if cond(reqs) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s：等 %s 未满足。实际流水：\n%s", want, timeout, formatReqs(reqs))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// formatReqs 把流水打成可读多行，供 t.Fatalf 与 -v 日志复用。
func formatReqs(reqs []wireReq) string {
	if len(reqs) == 0 {
		return "  （零个请求）"
	}
	base := reqs[0].At
	var b strings.Builder
	for i, r := range reqs {
		ext, _ := json.Marshal(r.Ext)
		body := r.Body
		if len(body) > 300 {
			body = body[:300] + "…"
		}
		fmt.Fprintf(&b, "  [%d] +%dms code=%d ext=%s body=%s\n",
			i, r.At.Sub(base).Milliseconds(), r.Code, ext, body)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (f *fakeEnd) serve() {
	for {
		c, err := f.lis.Accept()
		if err != nil {
			return
		}
		go f.handle(c)
	}
}

func (f *fakeEnd) handle(c net.Conn) {
	f.mu.Lock()
	f.open++
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.open--
		f.closes++
		f.mu.Unlock()
		_ = c.Close()
	}()

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

		// 帧头长度字段的最高字节是 codec 类型（JsonCodecs=0），所以要抹掉高 8 位。
		hdrLen := int32(binary.BigEndian.Uint32(frame[:4]) & 0xFFFFFF)
		if 4+int(hdrLen) > len(frame) {
			return
		}

		var req wireReq
		req.At = time.Now()
		if err := json.Unmarshal(frame[4:4+hdrLen], &req); err != nil {
			// 解码失败也要记账，并在 kind 上打标：帧的形状漂了的时候，断言里看到的是
			// 「一条 undecoded 请求」，而不是「零个请求」——前者指向假线/协议，
			// 后者会被误读成「映射那一行没落到 SDK」。
			req.Kind = f.kind + " (undecoded: " + err.Error() + ")"
		} else {
			req.Body = string(frame[4+hdrLen:])
			req.Kind = f.kind
		}
		f.record(req)

		if f.silent {
			continue
		}
		if err := f.reply(c, &req); err != nil {
			return
		}
	}
}

func (f *fakeEnd) reply(c net.Conn, req *wireReq) error {
	code := int16(0)
	remark := "fake: ok"
	var body []byte

	switch req.Code {
	case codeQueryRoute:
		if f.routeTo == "" {
			code = codeTopicNotExistResp
			remark = "fake: topic not exist"
		} else {
			// 路由体的形状照 SDK 的 gjson 解析路径来：brokerDatas 是数组，
			// brokerAddrs 是 {"0": "ip:port"}。少任何一个字段，客户端都会报路由解析失败。
			body = []byte(fmt.Sprintf(`{"queueDatas":[{"brokerName":"fakebroker",`+
				`"readQueueNums":2,"writeQueueNums":2,"perm":6,"topicSynFlag":0}],`+
				`"brokerDatas":[{"brokerName":"fakebroker","cluster":"DefaultCluster",`+
				`"brokerAddrs":{"0":%q}}]}`, f.routeTo))
		}
	case codeHeartbeat:
	default:
	}

	header, err := json.Marshal(map[string]any{
		"code":      code,
		"language":  "GO",
		"version":   317,
		"opaque":    req.Opaque,
		"flag":      1, // 应答位
		"remark":    remark,
		"extFields": map[string]string{},
	})
	if err != nil {
		return err
	}

	out := make([]byte, 8+len(header)+len(body))
	binary.BigEndian.PutUint32(out[0:4], uint32(4+len(header)+len(body)))
	binary.BigEndian.PutUint32(out[4:8], uint32(len(header)))
	copy(out[8:], header)
	copy(out[8+len(header):], body)

	_ = c.SetWriteDeadline(time.Now().Add(3 * time.Second))
	_, err = c.Write(out)
	return err
}

// clientIDOfHeartbeat 取心跳（code=34）body 里的 clientID。v2.1.2 的心跳 body 形状是
// `{clientID, producerDataSet[], consumerDataSet[]}`，本族只需要前两段的身份：
// instance_name 那一行唯一的直读口就是这里的 `ip@instance`（组名走发送请求的
// producerGroup，见 mapping_offline_test.go）。非心跳请求返回 ok=false。
func (r wireReq) clientIDOfHeartbeat() (string, bool) {
	if r.Code != codeHeartbeat {
		return "", false
	}
	var h struct {
		ClientID string `json:"clientID"`
	}
	if err := json.Unmarshal([]byte(r.Body), &h); err != nil || h.ClientID == "" {
		return "", false
	}
	return h.ClientID, true
}

// wantSignature 按 internal/remote.ACLInterceptor 的算法重算签名：
//
//	content = 拼接「{AccessKey=ak} ∪ 请求自带 extFields」按 key 字典序后的**值**
//	sig     = base64(HMAC-SHA1(secretKey, content + body))
//
// 两个容易踩的点，都在上游那段代码里：
//   - SecurityToken 只进 map、不进 order，所以**只有请求自己带它**时才会进 content；
//     路由查询不带 ⇒ 签名内容里没有 ST。所以本函数只用于路由查询，那条 content
//     恰好就是 `accessKey + topic`，和 Python 侧手算一致（本机实测钉过）。
//   - 线上看到的 extFields 已被拦截器补过 AccessKey/SecurityToken/Signature 三项，
//     所以「请求自带的集合」= 观测集合减掉这三项（SDK 的构造请求不会自己放这三个键）。
func wantSignature(accessKey, secretKey string, observedExt map[string]string, body string) string {
	values := map[string]string{"AccessKey": accessKey}
	keys := []string{"AccessKey"}
	for k, v := range observedExt {
		switch k {
		case "AccessKey", "SecurityToken", "Signature":
			continue
		}
		values[k] = v
		keys = append(keys, k)
	}
	sort.Strings(keys)

	content := make([]byte, 0, 64)
	for _, k := range keys {
		content = append(content, values[k]...)
	}
	content = append(content, body...)

	mac := hmac.New(sha1.New, []byte(secretKey))
	mac.Write(content)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// uniqInstance 给每条腿一个唯一的 instance_name。
// 必须唯一：SDK 的 rmqClient 按 clientID = `ip@instanceName` 进程内共享
// （internal/client.go 的 GetOrNewRocketMQClient），两条腿撞同一个 instance 时会
// 共用一台「名字服务客户端」，前一条腿的订阅与路由缓存会污染后一条的读数。
func uniqInstance(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}
