// nacos 后端实例清单的**独立**读口：走 nacos 的 HTTP 查询口，不用注册用的 SDK。
//
// 为什么要有这个文件：用 SDK 读 SDK 写只能证明 SDK 自洽（本地缓存、订阅推送、
// 名字拼接都在同一份代码里），证明不了服务端那份服务下真有/没有实例。停机摘除、
// 命名后缀这类判据必须绕开被测实现读后端。
//
// 无构建约束：`-tags nacos` 的装配用例和默认/`-tags grpcgw` 的后端语义用例都要用它，
// 而两者分属不同的 tag 集合——放在任一边都会在另一个形态里编不过。
package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// nacosHost 是 nacos v1 instance/list 回包 hosts[] 里的一个实例。
//
// 字段表按实测回包写（registry/nacos 探针）：hosts 项只有 healthy / enabled /
// ephemeral，`valid` 在**回包顶层**而不是每项上——把它放进本结构会得到一个恒假的
// 零值，判据于是恒红。
type nacosHost struct {
	IP        string `json:"ip"`
	Port      uint64 `json:"port"`
	Healthy   bool   `json:"healthy"`
	Enabled   bool   `json:"enabled"`
	Ephemeral bool   `json:"ephemeral"`
}

type nacosHosts []nacosHost

// assertMatches 把后端看到的地址钉回本进程的真实监听地址：端口不一致说明注册进去
// 的不是这个实例（配置里的默认值、或上一轮残留）。
func (h nacosHosts) assertMatches(t *testing.T, scheme, addr string) {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("%s 面的监听地址 %q 不合法: %v", scheme, addr, err)
	}
	for _, host := range h {
		if fmt.Sprint(host.Port) == port && host.IP == strings.Split(addr, ":")[0] {
			return
		}
	}
	t.Fatalf("%s 面注册的实例里没有本进程的 %s: %+v", scheme, addr, h)
}

// listNacosHostsAt 查 addr 上 namespace ns 下 service 这个服务名的实例清单。
// ns 传空串按 nacos 的默认租户查：命名 API 里 "public" 与 "" 是同一个租户
// （registry/nacos 实测，双向可见），所以调用方按契约里写的那个值原样传即可。
func listNacosHostsAt(t *testing.T, addr, ns, service string) nacosHosts {
	t.Helper()
	q := url.Values{
		"serviceName": {service},
		"namespaceId": {ns},
		"groupName":   {"DEFAULT_GROUP"},
		"healthyOnly": {"false"},
	}
	u := "http://" + addr + "/nacos/v1/ns/instance/list?" + q.Encode()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		t.Fatalf("new request %s: %v", u, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("query nacos %s: %v", service, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("query nacos %s: %s", service, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatalf("read nacos %s body: %v", service, err)
	}
	var out struct {
		Hosts nacosHosts `json:"hosts"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode nacos %s 回包: %v（原文 %s）", service, err, body)
	}
	return out.Hosts
}
