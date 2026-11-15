// Package compose 提供 `bald compose`：把本地集成依赖（etcd / consul / redis /
// etcd/redis/nacos）收敛成一份可复现的 compose 定义，并代为调用 docker compose。
//
// 为什么需要它：README 里起集成依赖是三段手写 `docker run`（还带 `--platform`
// 与 `-e MODE=standalone` 这类容易抄错的参数），CI 的 integration 作业里是另一份
// yaml。两处各写一遍，第三处（AI 会话）就只好再发明一次。这里的目录**逐项对齐
// CI `.github/workflows/ci.yml` 的 services 块**——镜像 tag、端口映射、env 全部
// 同源，默认地址也与之相同，所以「本地跑通的集成用例」在 CI 里是同一种环境。
//
// 边界（不做的事）：
//
//   - 不合并用户自己的 compose.yaml。合并语义（谁覆盖谁）比命令本身更难解释，
//     用户要定制就 `compose config --out` 落一份自己的文件，再用 --file 指过去。
//   - 不管生命周期编排（依赖顺序、健康等待）。`up -d` 之后就返回；判据由测试
//     自己探活（与 CI 的「探不到即失败」同一口径：环境问题不该被 Skip 掩埋）。
//   - 不内嵌 docker SDK。调用 docker CLI 是可审计的（--dry-run 能打印完整 argv），
//     也不把一个几十 MB 的客户端依赖拖进 4MB 的 CLI。
package compose

import (
	"fmt"
	"sort"
	"strings"
)

// 网络与服务标识：项目名固定，便于 down 一次清干净；容器名加前缀避免与用户
// 自己的 etcd/redis 抢名字。
const (
	projectName = "bald-infra"
	// defaultDocker 是 docker CLI 名（可用 --docker 指向 podman 等兼容实现）。
	defaultDocker = "docker"
)

// kv 是有序的环境变量（map 的遍历序会让生成物不确定，compose 文件必须可 diff）。
type kv struct {
	Key   string
	Value string
}

// service 是本地集成依赖的一项。
//
// 字段刻意只保留 compose 里会用到的那些：镜像、平台、命令、端口、环境变量。
// 不引入 volume/healthcheck 的理由是「多一个字段就多一处与 CI 漂移的地方」——
// 这三个镜像都不需要持久化（都用内存/内嵌存储），健康判定统一交给探活测试。
type service struct {
	Name     string
	Image    string
	Platform string   // 非空时写 platform:（nacos 官方镜像只有 amd64）
	Command  []string // 非空时覆盖镜像 CMD
	Ports    []string // "host:container"（只绑 127.0.0.1，不对外暴露）
	Env      []kv
	// Addr 是本地默认访问地址（--dry-run/status 的提示用，也是各测试的默认值）。
	Addr string
	// Note 解释「为什么是这些参数」（抄参数的人最需要的那句话）。
	Note string
}

// catalog 是全部可起的服务（声明序 = list/config 的输出序）。
//
// 逐项对齐 .github/workflows/ci.yml 的 integration 作业 services：镜像 tag 与
// 端口映射必须一致，否则「本地绿、CI 红」会变成常态。
var catalog = []service{
	{
		Name:  "etcd",
		Image: "quay.io/coreos/etcd:v3.5.14",
		Ports: []string{"127.0.0.1:2379:2379"},
		Env:   []kv{{"ETCD_LISTEN_CLIENT_URLS", "http://0.0.0.0:2379"}, {"ETCD_ADVERTISE_CLIENT_URLS", "http://127.0.0.1:2379"}},
		Addr:  "127.0.0.1:2379",
		Note:  "etcd 的 ETCD_* 环境变量与命令行 flag 等价，镜像 entrypoint 直接读",
	},
	{
		Name:  "redis",
		Image: "redis:7-alpine",
		Ports: []string{"127.0.0.1:6379:6379"},
		Addr:  "redis://127.0.0.1:6379",
	},
	{
		Name:     "nacos",
		Image:    "nacos/nacos-server:v2.3.2",
		Platform: "linux/amd64",
		Ports:    []string{"127.0.0.1:8848:8848", "127.0.0.1:9848:9848"},
		Env: []kv{
			{"MODE", "standalone"},
			{"NACOS_AUTH_ENABLE", "false"},
			{"JVM_XMS", "256m"},
			{"JVM_XMX", "512m"},
		},
		Addr: "127.0.0.1:8848",
		Note: "9848 必须一起映射：SDK v2 的 naming 走 gRPC（8848+1000），只映射 8848 会静默注册不上",
	},
}

// Services 返回可用服务名（声明序，供 list 与错误提示用）。
func Services() []string {
	out := make([]string, 0, len(catalog))
	for _, s := range catalog {
		out = append(out, s.Name)
	}
	return out
}

// lookup 按名字取服务，未知名字给出可选清单（错误信息要能直接改对）。
func lookup(name string) (service, error) {
	for _, s := range catalog {
		if s.Name == name {
			return s, nil
		}
	}
	return service{}, fmt.Errorf("unknown service %q (available: %s)", name, strings.Join(Services(), ", "))
}

// selectServices 把位置参数解析成服务集合：空 = 全量；名字保序去重；未知名报错。
//
// 逗号分隔的整串（"etcd,redis"）按多服务拆分——Taskfile 的 SVC=etcd,redis 会把
// 逗号串原样传进来（`compose up {{.SVC}}`），按单个未知名报错等于文档照抄即错
// （adv-surface 发现 3）。--service flag 的逗号语义与此一致。
func selectServices(names []string) ([]service, error) {
	if len(names) == 0 {
		return catalog, nil
	}
	seen := map[string]bool{}
	var out []service
	for _, raw := range names {
		for _, name := range strings.Split(raw, ",") {
			name = strings.TrimSpace(name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			s, err := lookup(name)
			if err != nil {
				return nil, err
			}
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no service selected (available: %s)", strings.Join(Services(), ", "))
	}
	return out, nil
}

// renderYAML 生成 compose 定义的 YAML 文本。
//
// 手写而非 yaml.Marshal：输出要**逐字节确定**且键序固定（image → platform →
// command → ports → environment），这样 `compose config` 的产物能进 diff、能当
// 契约断言；struct 里的 map 序列化会按字母序打乱可读性最强的「镜像在最上」。
func renderYAML(services []service) string {
	var b strings.Builder
	b.WriteString("# 由 bald compose 生成：本地集成依赖（与 CI integration 作业的 services 同源）。\n")
	b.WriteString("# 定制请 `bald compose config --out compose.yaml` 后改那份，再用 --file 指过去。\n")
	fmt.Fprintf(&b, "name: %s\n", projectName)
	b.WriteString("services:\n")
	for _, s := range services {
		fmt.Fprintf(&b, "  %s:\n", s.Name)
		fmt.Fprintf(&b, "    image: %s\n", s.Image)
		if s.Platform != "" {
			fmt.Fprintf(&b, "    platform: %s\n", s.Platform)
		}
		if len(s.Command) > 0 {
			fmt.Fprintf(&b, "    command: [%s]\n", quotedList(s.Command))
		}
		if len(s.Ports) > 0 {
			fmt.Fprintf(&b, "    ports: [%s]\n", quotedList(s.Ports))
		}
		if len(s.Env) > 0 {
			b.WriteString("    environment:\n")
			for _, e := range s.Env {
				fmt.Fprintf(&b, "      %s: %q\n", e.Key, e.Value)
			}
		}
		if s.Note != "" {
			fmt.Fprintf(&b, "    # %s\n", s.Note)
		}
	}
	return b.String()
}

// quotedList 把切片渲染成 YAML 流式序列（双引号包裹，端口映射不会被误读成数字）。
func quotedList(items []string) string {
	quoted := make([]string, len(items))
	for i, v := range items {
		quoted[i] = fmt.Sprintf("%q", v)
	}
	return strings.Join(quoted, ", ")
}

// listText 是 `compose list` 的人读表格（AI 也能解析：每行 名字<TAB>镜像<TAB>地址）。
func listText() string {
	var b strings.Builder
	b.WriteString("service\timage\tlocal address\n")
	for _, s := range catalog {
		fmt.Fprintf(&b, "%s\t%s\t%s\n", s.Name, s.Image, s.Addr)
	}
	return b.String()
}

// sortedNames 返回排序后的名字（仅用于稳定输出，如错误与摘要）。
func sortedNames(services []service) []string {
	out := make([]string, 0, len(services))
	for _, s := range services {
		out = append(out, s.Name)
	}
	sort.Strings(out)
	return out
}
