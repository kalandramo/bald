package kubernetes

// registry_integration_test.go —— kubernetes 后端的真机用例（对接真实
// kube-apiserver + kubelet，本地用 kind，CI 用 integration-kubernetes 作业）。
//
// 为什么必须真机而不是 fake clientset：Register/Deregister 用的是
// types.StrategicMergePatchType，而 fake clientset 不实现 strategic-merge 的
// 键删除语义（值 null 删键）——用 fake 跑「标签被清掉了」这条断言，判的是
// 假的东西。同理，informer 的 watch 事件序列（Add/Update 何时进
// FilteringResourceEventHandler）只有真 apiserver 会给。
//
// 与集群内的差别（这条腿覆盖不到的部分，写在登记册 §7.1 里）：
//   - New() 走 rest.InClusterConfig，只有 Pod 内才有；本腿一律用
//     NewWithClient 注入（就是包注释给的「集群外」正道）。
//   - GetNamespace() 读 /var/run/secrets/.../namespace 的包级变量在 Pod 外为空，
//     所以测试在包内直接把它指向本腿自己创建的 namespace。
//
// 触发条件：非 -short 且 kubeconfig 可用（KUBECONFIG 或 ~/.kube/config）；
// 两者缺一即 t.Skip，与 etcd/consul 那两条腿同一「不可达即跳」口径——因此
// CI 的 integration-kubernetes 作业靠 EXPECTED 判据兜住「跳过不等于跑过」。

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	registry "github.com/kalandramo/bald/registry"
)

const (
	// e2eNamespace 是本腿专用的固定 namespace（不存在即创建，Pod 逐条清理）。
	e2eNamespace = "bald-e2e-registry"
	// 容器镜像默认用 kind 节点里已经带的那一个；换后端集群时用环境变量指一个
	// 「能起来且不会退出」的镜像（pause 就是干这个的）。
	pauseImageEnv = "BALD_E2E_POD_IMAGE"
	defaultImage  = "registry.k8s.io/pause:3.10"
)

func podImage() string {
	if v := strings.TrimSpace(os.Getenv(pauseImageEnv)); v != "" {
		return v
	}
	return defaultImage
}

func newTestClient(t *testing.T) *kubernetes.Clientset {
	t.Helper()
	if testing.Short() {
		t.Skip("集成测试：依赖真实 kube-apiserver，-short 下跳过")
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		t.Skipf("没有可用 kubeconfig（KUBECONFIG / ~/.kube/config），跳过: %v", err)
	}
	// 显式探活：client-go 的构造是惰性的，集群不可达要到这里才暴露。
	cfg.Timeout = 10 * time.Second
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Skipf("构造 clientset 失败，跳过: %v", err)
	}
	if _, err := client.Discovery().ServerVersion(); err != nil {
		t.Skipf("kube-apiserver 不可达，跳过（环境缺失）: %v", err)
	}
	ensureNamespace(t, client)
	return client
}

func ensureNamespace(t *testing.T, client *kubernetes.Clientset) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: e2eNamespace},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("创建 namespace %s: %v", e2eNamespace, err)
	}
}

// startTestRegistry 用注入的 clientset 构造并启动 Registry，同时把包级的
// currentNamespace / HOSTNAME 指向本腿自建的那个 namespace 与目标 Pod——
// 这两个值在集群内由 ServiceAccount 与 Pod 名提供，集群外只能由测试提供。
func startTestRegistry(t *testing.T, client *kubernetes.Clientset, podName string) *Registry {
	t.Helper()
	restore := currentNamespace
	currentNamespace = e2eNamespace
	t.Cleanup(func() { currentNamespace = restore })

	t.Setenv("HOSTNAME", podName)

	r := NewWithClient(client, WithNamespace(e2eNamespace))
	r.Start()
	if !r.podInformer.HasSynced() {
		r.Close()
		t.Fatal("informer 缓存没同步（RBAC 不允许 list pods？或 apiserver 慢）——后面的断言会在空缓存上假绿")
	}
	t.Cleanup(r.Close)
	return r
}

// newRunningPod 创建一个真能跑起来的 Pod（带两个命名端口），等它进入 Running
// 且拿到 PodIP 后返回。端口名故意起成 tcp-*/无协议含义的样子，好让「协议来自
// 注册时写的 annotation，而不是端口名」这条判据有反证。
func newRunningPod(t *testing.T, client *kubernetes.Clientset, name string, labels map[string]string) *corev1.Pod {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: e2eNamespace, Labels: labels},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:            "pause",
				Image:           podImage(),
				ImagePullPolicy: corev1.PullIfNotPresent,
				Ports: []corev1.ContainerPort{
					{Name: "port-8000", ContainerPort: 8000, Protocol: corev1.ProtocolTCP},
					{Name: "port-8001", ContainerPort: 8001, Protocol: corev1.ProtocolTCP},
				},
			}},
		},
	}
	created, err := client.CoreV1().Pods(e2eNamespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("创建 Pod %s: %v", name, err)
	}
	t.Cleanup(func() {
		del := context.Background()
		_ = client.CoreV1().Pods(e2eNamespace).Delete(del, created.Name, metav1.DeleteOptions{})
	})
	return waitPodRunning(t, client, created.Name)
}

func waitPodRunning(t *testing.T, client *kubernetes.Clientset, name string) *corev1.Pod {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		pod, err := client.CoreV1().Pods(e2eNamespace).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Get Pod %s: %v", name, err)
		}
		if pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP != "" {
			return pod
		}
		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			t.Fatalf("Pod %s 直接进了 %s，等不到 Running", name, pod.Status.Phase)
		}
		time.Sleep(500 * time.Millisecond)
	}
	pod, _ := client.CoreV1().Pods(e2eNamespace).Get(context.Background(), name, metav1.GetOptions{})
	msg := "无"
	if pod != nil {
		msg = podContainerMessage(pod)
	}
	t.Fatalf("Pod %s 在 90s 内没进入 Running（镜像 %s，容器信息：%s）", name, podImage(), msg)
	return nil
}

func podContainerMessage(pod *corev1.Pod) string {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			return fmt.Sprintf("%s: %s", cs.State.Waiting.Reason, cs.State.Waiting.Message)
		}
	}
	return string(pod.Status.Phase)
}

func readPod(t *testing.T, client *kubernetes.Clientset, name string) *corev1.Pod {
	t.Helper()
	pod, err := client.CoreV1().Pods(e2eNamespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get Pod %s: %v", name, err)
	}
	return pod
}

// TestRegister_WritesPodIdentityAndDiscovery 走完整的注册面：Register 把
// id/name/version + metadata/protocols 两段 annotation 打到 Pod 上（strategic
// merge 的真实语义只有真 apiserver 能判），发现侧从 informer 缓存里把它读回来，
// 端点协议取 annotation 而不是端口名。
func TestRegister_WritesPodIdentityAndDiscovery(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	svcName := fmt.Sprintf("ut-k8s-reg-%d", time.Now().UnixNano())
	podName := svcName + "-pod"
	pod := newRunningPod(t, client, podName, nil)

	r := startTestRegistry(t, client, podName)

	// Watch 必须在 Register 之前建：informer 的 handler 只对「注册之后」发生的
	// 事件回调，先注册再 Watch 就只剩空等。
	w, err := r.Watch(ctx, svcName)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = w.Stop() }()

	inst := &registry.ServiceInstance{
		ID:        "11111111-2222-3333-4444-555555555555",
		Name:      svcName,
		Version:   "v9.9.9",
		Metadata:  map[string]string{"region": "sh", "zone": "sh001"},
		Endpoints: []string{fmt.Sprintf("http://%s:8000", pod.Status.PodIP), fmt.Sprintf("grpc://%s:8001", pod.Status.PodIP)},
	}
	if err := r.Register(ctx, inst); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// 后端真实状态核对（不经抽象层）：标签三段 + annotation 两段都在 Pod 上。
	got := readPod(t, client, podName)
	if got.Labels[LabelsKeyServiceID] != inst.ID {
		t.Errorf("标签 %s = %q, want %q", LabelsKeyServiceID, got.Labels[LabelsKeyServiceID], inst.ID)
	}
	if got.Labels[LabelsKeyServiceName] != svcName {
		t.Errorf("标签 %s = %q, want %q", LabelsKeyServiceName, got.Labels[LabelsKeyServiceName], svcName)
	}
	if got.Labels[LabelsKeyServiceVersion] != "v9.9.9" {
		t.Errorf("标签 %s = %q, want v9.9.9", LabelsKeyServiceVersion, got.Labels[LabelsKeyServiceVersion])
	}
	if meta := got.Annotations[AnnotationsKeyMetadata]; !strings.Contains(meta, `"region":"sh"`) {
		t.Errorf("annotation %s = %q，不含 region=sh", AnnotationsKeyMetadata, meta)
	}
	if pm := got.Annotations[AnnotationsKeyProtocolMap]; !(strings.Contains(pm, `"8000":"http"`) && strings.Contains(pm, `"8001":"grpc"`)) {
		t.Errorf("annotation %s = %q，端口→协议映射没写成 {8000:http, 8001:grpc}", AnnotationsKeyProtocolMap, pm)
	}

	// 发现侧：从 informer 缓存读回来，端点由 PodIP + 容器端口 + 协议映射拼出。
	// 端口名故意是 port-8000，若不按 annotation 走就会拼成 port://。
	list, err := waitForInstances(t, r, svcName, 30*time.Second, func(ss []*registry.ServiceInstance) bool {
		return len(ss) == 1
	})
	if err != nil {
		t.Fatalf("GetService 轮询: %v", err)
	}
	found := list[0]
	if found.ID != inst.ID || found.Version != "v9.9.9" {
		t.Errorf("发现实例 ID/Version = %q/%q, want %q/v9.9.9", found.ID, found.Version, inst.ID)
	}
	if found.Metadata["region"] != "sh" || found.Metadata["zone"] != "sh001" {
		t.Errorf("发现实例 Metadata = %v，annotation 里的两段没解回来", found.Metadata)
	}
	wantEP := []string{fmt.Sprintf("http://%s:8000", pod.Status.PodIP), fmt.Sprintf("grpc://%s:8001", pod.Status.PodIP)}
	if strings.Join(found.Endpoints, ",") != strings.Join(wantEP, ",") {
		t.Errorf("发现实例 Endpoints = %v, want %v（协议必须来自 bald-service-protocols，而不是端口名 port-8000）",
			found.Endpoints, wantEP)
	}

	// Watch 侧：注册产生的 Update 事件要把同一份快照推到订阅者手上。
	nextCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	announced, err := w.Next(nextCtx)
	if err != nil {
		t.Fatalf("Watch.Next: %v", err)
	}
	if len(announced) != 1 || announced[0].ID != inst.ID {
		t.Fatalf("Watch.Next = %v, want 一条 ID=%s 的实例", idsOf(announced), inst.ID)
	}
}

// TestDeregister_RemovesPodFromDiscovery 钉住注销的语义：把标签清掉，于是
// 发现侧再也列不到这个 Pod。
//
// 这条用例落地的第一时间是红的——Deregister 的实现是
// `Register(ctx, &registry.ServiceInstance{Metadata: map[string]string{}})`，
// 而 Register 的首行是 ServiceInstance.Validate 闸门（3647d71 引入），空实例
// 必然被拒，于是 k8s 后端的注销从来就没有生效过（且按 §4.1 的口径，停机时
// 这个错误是要报出来的）。
func TestDeregister_RemovesPodFromDiscovery(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	svcName := fmt.Sprintf("ut-k8s-dereg-%d", time.Now().UnixNano())
	podName := svcName + "-pod"
	pod := newRunningPod(t, client, podName, nil)

	r := startTestRegistry(t, client, podName)
	inst := &registry.ServiceInstance{
		ID:        "dereg-id-1",
		Name:      svcName,
		Version:   "v1",
		Metadata:  map[string]string{"k": "v"},
		Endpoints: []string{fmt.Sprintf("http://%s:8000", pod.Status.PodIP)},
	}
	if err := r.Register(ctx, inst); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := waitForInstances(t, r, svcName, 30*time.Second, func(ss []*registry.ServiceInstance) bool {
		return len(ss) == 1
	}); err != nil {
		t.Fatalf("注销前应能发现该实例: %v", err)
	}

	if err := r.Deregister(ctx, inst); err != nil {
		t.Fatalf("Deregister: %v", err)
	}

	// 反证「注销只是没写成功」：标签与 annotation 都得从 Pod 上消失。
	got := readPod(t, client, podName)
	for _, k := range []string{LabelsKeyServiceID, LabelsKeyServiceName, LabelsKeyServiceVersion} {
		if v, ok := got.Labels[k]; ok {
			t.Errorf("注销后标签 %s 仍是 %q", k, v)
		}
	}
	for _, k := range []string{AnnotationsKeyMetadata, AnnotationsKeyProtocolMap} {
		if v, ok := got.Annotations[k]; ok {
			t.Errorf("注销后 annotation %s 仍是 %q", k, v)
		}
	}

	// 发现侧不再列出它（lister 的选择器按 bald-service-app 匹配，标签没了就查不到）。
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		list, err := r.GetService(ctx, svcName)
		if err != nil {
			t.Fatalf("GetService: %v", err)
		}
		if len(list) == 0 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("注销 30s 后仍能发现 %s —— 注销没生效", svcName)
}

// TestGetService_ExcludesNonRunningPod 钉住「只报 Running 实例」这一层：一个
// 永远调度不到的 Pod（nodeName 指向不存在的节点）会停在 Pending，标签照样
// 由测试打上，但发现侧必须把它排除——否则流量会打到一个没有 IP 的实例。
func TestGetService_ExcludesNonRunningPod(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	svcName := fmt.Sprintf("ut-k8s-pending-%d", time.Now().UnixNano())
	running := newRunningPod(t, client, svcName+"-run", map[string]string{
		LabelsKeyServiceID:      "run-1",
		LabelsKeyServiceName:    svcName,
		LabelsKeyServiceVersion: "v1",
	})
	pendingName := svcName + "-pend"
	_, err := client.CoreV1().Pods(e2eNamespace).Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: pendingName, Namespace: e2eNamespace, Labels: map[string]string{
			LabelsKeyServiceID:      "pend-1",
			LabelsKeyServiceName:    svcName,
			LabelsKeyServiceVersion: "v1",
		}},
		Spec: corev1.PodSpec{
			NodeName:   "bald-e2e-no-such-node",
			Containers: []corev1.Container{{Name: "pause", Image: podImage(), ImagePullPolicy: corev1.PullIfNotPresent}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("创建 Pending Pod: %v", err)
	}
	t.Cleanup(func() {
		_ = client.CoreV1().Pods(e2eNamespace).Delete(context.Background(), pendingName, metav1.DeleteOptions{})
	})

	r := startTestRegistry(t, client, running.Name)
	list, err := waitForInstances(t, r, svcName, 30*time.Second, func(ss []*registry.ServiceInstance) bool {
		return len(ss) >= 1
	})
	if err != nil {
		t.Fatalf("GetService 轮询: %v", err)
	}
	for _, in := range list {
		if in.ID == "pend-1" {
			t.Fatalf("Pending 的 Pod 也被当成实例返回了（%v）——phase 过滤失效，端点会是空 IP", idsOf(list))
		}
	}
	if len(list) != 1 || list[0].ID != "run-1" {
		t.Fatalf("实例列表 = %v, want 仅 run-1", idsOf(list))
	}
}

// waitForInstances 轮询 GetService 直到谓词成立或超时；informer 缓存的收敛
// 时间不是常数，所以发现类断言一律走有界重试。
func waitForInstances(t *testing.T, r *Registry, name string, timeout time.Duration,
	pred func([]*registry.ServiceInstance) bool) ([]*registry.ServiceInstance, error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last []*registry.ServiceInstance
	for {
		list, err := r.GetService(context.Background(), name)
		if err != nil {
			return nil, err
		}
		last = list
		if pred(list) {
			return list, nil
		}
		if time.Now().After(deadline) {
			return last, fmt.Errorf("超时：GetService(%s) 最后一次 = %v", name, idsOf(last))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func idsOf(list []*registry.ServiceInstance) []string {
	out := make([]string, 0, len(list))
	for _, in := range list {
		out = append(out, in.ID)
	}
	return out
}
