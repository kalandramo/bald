//go:build nacos

// 装配侧的真机证据（opt-in）：`-tags nacos` 构建里，注册中心是不是**真的**按契约
// registry 段接到了 nacos。
//
// discovery_registry_e2e_test.go 那三条证明的是后端语义本身（按契约段直接构造
// Registrar/Discovery，与业务装配无关）；本用例证明的是另一件事：**装配**。默认
// 构建把 registry 段程序化接管（appkit.Registrar(inmemory)），于是「契约选了
// nacos」在该形态里本来就不该生效；nacos 变体不钉显式实例，注册才可能真的走到
// 后端。这里判的就是那一步有没有发生：
//
//  1. 起来的应用把两个面都注册进了真 nacos，服务名带 scheme 后缀
//     （`<app>.grpc` / `<app>.http`，命名口径见 TestNacosDiscoveryNameCarriesSchemeSuffix），
//     且实例地址就是本进程实际监听的端口——不是内存实例，也不是配置里写的 :9090。
//  2. 优雅停机后后端不再留有本进程的实例（终态守卫；它判不到「Deregister 调没调」也
//     判不到「client 关没关」——那两条机制判据在 registry/nacos/contract/
//     instance_lifecycle_e2e_test.go（在 integration 作业里），本用例自己的那条只在
//     `example` 作业里跑单条装配，见下面 stop 处的实测）。
//
// namespace 用契约写的 `public`：命名 API 里它与默认租户是**同一个**租户
// （registry/nacos 实测：ns=public 注册的实例用 ns="" 也查得到，反之亦然），
// 所以这里按 public 查得到即说明契约值原样生效，不需要像配置 API 那样折成空串。
//
// 后端不可达即 Skip（环境缺失，不是验证失败）。地址跟着契约文件走：
// configs/bald-demo-api.yaml 里钉的 `registry.nacos.server_addrs` 就是本用例查的
// 地址（下面有漂移判据）——这条用例测的正是「那份契约接没接上」，所以
// BALD_E2E_NACOS_ADDR 对它无效（那只作用于按段构造的 discovery 用例）。
//
//	cd _example/bald
//	go test -tags nacos ./internal/bootstrap/ -run TestNacosAssemblyRegistersIntoBackend -v
//
//	# nacos:  docker run -d -p 127.0.0.1:8848:8848 -p 127.0.0.1:9848:9848 \
//	#           -e MODE=standalone -e NACOS_AUTH_ENABLE=false nacos/nacos-server:v2.3.2
//	#           # v2.3.2 无 arm64 manifest：Apple Silicon 加 --platform linux/amd64
package bootstrap

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// nacosContractAddr 是 configs/bald-demo-*.yaml 里 registry.nacos.server_addrs
// 钉死的地址；nacosContractNS 是同文件的 registry.nacos.namespace。
// 值由 TestNacosAssemblyRegistersIntoBackend 之前的漂移判据守住。
const (
	nacosContractAddr = "127.0.0.1:8848"
	nacosContractNS   = "public"
)

func TestNacosAssemblyRegistersIntoBackend(t *testing.T) {
	if testing.Short() {
		t.Skip("集成测试：依赖真实 nacos，-short 下跳过")
	}

	// 漂移判据：本用例查的后端跟的是契约文件里的值，那份值改了就必须同步改这里，
	// 否则测的是「查不到」而不是「没注册」。
	docs, err := os.ReadFile(filepath.Join("..", "..", "configs", "bald-demo-api.yaml"))
	if err != nil {
		t.Fatalf("读契约文件: %v", err)
	}
	for _, want := range []string{nacosContractAddr, "namespace: " + nacosContractNS, "type: nacos"} {
		if !strings.Contains(string(docs), want) {
			t.Fatalf("configs/bald-demo-api.yaml 里已不含 %q：契约的注册中心接线变了，本用例的地址/命名空间常量要跟着改", want)
		}
	}

	probe, err := net.DialTimeout("tcp", nacosContractAddr, 3*time.Second)
	if err != nil {
		t.Skipf("nacos %s 不可达，跳过（环境缺失）: %v", nacosContractAddr, err)
	}
	_ = probe.Close()

	// 服务名唯一化：共用一台本机 nacos 时，多次运行/别的用例的残留不该互相干扰；
	// 停机摘除那条判据尤其依赖「只有本进程在这个名字下注册过」。
	// 前缀仍是 BALD_DEMO_：env 前缀由契约文件的 app.name 先探后挂（serve.go），
	// 覆盖 app.name 不改前缀。
	appName := fmt.Sprintf("bald-demo-asm-%d", time.Now().UnixNano())
	t.Setenv("BALD_DEMO_APP_NAME", appName)

	// extra=nil：不钉显式 Registrar，注册中心完全按 Options(RoleAPI) 在这个构建
	// 形态下的实际接线走——这正是与 startRole/startRoleWithConfig 的唯一区别。
	grpcAddr, httpAddr, stop := startRoleWithOptions(t, RoleAPI, "", nil)

	grpcHosts := waitForNacosHosts(t, appName+".grpc", 1, 30*time.Second)
	grpcHosts.assertMatches(t, "grpc", grpcAddr)
	httpHosts := waitForNacosHosts(t, appName+".http", 1, 30*time.Second)
	httpHosts.assertMatches(t, "http", httpAddr)

	// 停机终态：后端里不再留着本进程的实例。留在注册中心里的死地址就是生产事故。
	//
	// 这条**只是终态守卫**，判不到具体哪条摘除路径：实测（nacos v2.3.2，
	// registry/nacos 探针）ephemeral 实例随 naming client 的连接断开被服务端摘掉
	// （Close 后 0.5–0.6s 归零），显式 Deregister 也是同一量级；client 不关**且**不注销
	// 时心跳把实例续到 75s 之后仍在。所以它是两条路径的**或**——把 Deregister 的服务名
	// 后缀去掉，或把契约 Provider 的 cleanup 改成空转，这里都照样绿（两轮变异实测均如此）。
	// 两条机制各自钉在 registry/nacos/contract/instance_lifecycle_e2e_test.go：一条只注销、
	// client 保持打开，另一条只调 cleanup、不注销。
	//
	// 窗口 15s：实测任一单条摘除路径 ≤0.6s，两条都没发生时 75s 仍在——中间隔着两个量级。
	//
	// **两个面各自都要轮询，不能只等 grpc 归零后一次性查 http**：服务端按
	// `<服务名>.<scheme>` 分开存这两个实例，摘除是两次独立的删除事件，不保证同一拍落地。
	// 本机实测（同一条用例隔离跑 8 次，红 1 次）就是栽在单查那一下——grpc 侧已归零、
	// http 侧还差一拍，`{IP:127.0.0.1 Port:59669 Healthy:true Ephemeral:true}` 被断成
	// 「HTTP 面的注册没随停机释放」。断言的强度没变（仍是「停机后 15s 内归零」），变的
	// 是它不再要求两个面同刻消失。
	stop()
	waitNacosHostsGone(t, appName+".grpc", 15*time.Second)
	waitNacosHostsGone(t, appName+".http", 15*time.Second)
}

// waitNacosHostsGone 轮询到该服务名在该后端上没有任何实例为止；超时即判红
// （留在注册中心里的死地址就是生产事故）。
func waitNacosHostsGone(t *testing.T, service string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		h := listNacosHostsAt(t, nacosContractAddr, nacosContractNS, service)
		if len(h) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("停机 %d 秒后 %s 仍有 %d 个实例（注册中心 client 没关，心跳还在续租）: %+v",
				int(timeout.Seconds()), service, len(h), h)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitForNacosHosts 轮询到后端在该服务名下有 want 个实例为止；超时即判红。
func waitForNacosHosts(t *testing.T, service string, want int, timeout time.Duration) nacosHosts {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last nacosHosts
	for {
		last = listNacosHostsAt(t, nacosContractAddr, nacosContractNS, service)
		if len(last) >= want {
			for _, h := range last {
				// healthy=心跳到位、enabled=没被手工摘掉、ephemeral=进程没摘干净时
				// 服务端会自己过期（否则死地址永久留在注册中心里）。
				if !h.Healthy || !h.Enabled || !h.Ephemeral {
					t.Fatalf("%s 的实例状态不对（注册参数或心跳没到位）: %+v", service, h)
				}
			}
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("装配后 %d 秒内 %s 在 nacos %s 上始终只有 %d 个实例（注册没发生）: %+v",
				int(timeout.Seconds()), service, nacosContractAddr, len(last), last)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
