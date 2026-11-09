package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const kubeconfigTemplate = `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: %s
contexts:
- name: ctx
  context:
    cluster: c
    user: u
current-context: ctx
users:
- name: u
  user: {}
`

func writeKubeconfig(t *testing.T, server string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(fmt.Sprintf(kubeconfigTemplate, server)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testConfigMap(ns, name string, data map[string]string) *v1.ConfigMap {
	return &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Data:       data,
	}
}

// newFakeKube 返回挂载伪 clientset 的 kube；watch 由假 watcher 驱动，
// 便于按顺序喂入事件验证过滤/派发/终止分支。
func newFakeKube(t *testing.T, objs ...runtime.Object) (*kube, *watch.RaceFreeFakeWatcher) {
	t.Helper()
	cs := fake.NewSimpleClientset(objs...)
	fw := watch.NewRaceFreeFake()
	cs.PrependWatchReactor("configmaps", func(k8stesting.Action) (bool, watch.Interface, error) {
		return true, fw, nil
	})
	return &kube{client: cs}, fw
}

func TestOptionsSetters(t *testing.T) {
	k := New(
		WithLabelSelector("app=a"),
		WithFieldSelector("metadata.name=b"),
		WithKubeConfig("/tmp/config"),
		WithMaster("https://master"),
	)
	if k.opts.LabelSelector != "app=a" || k.opts.FieldSelector != "metadata.name=b" ||
		k.opts.KubeConfig != "/tmp/config" || k.opts.Master != "https://master" {
		t.Errorf("opts = %+v", k.opts)
	}
}

func TestInit_KubeConfigSuccess(t *testing.T) {
	k := New(WithKubeConfig(writeKubeconfig(t, "http://127.0.0.1:1")))
	if err := k.init(); err != nil {
		t.Fatalf("init() = %v", err)
	}
	if k.client == nil {
		t.Fatal("init() 未建立 client")
	}
}

func TestInit_KubeConfigError(t *testing.T) {
	k := New(WithKubeConfig(filepath.Join(t.TempDir(), "missing")))
	if err := k.init(); err == nil {
		t.Fatal("kubeconfig 不存在时 init 应当报错")
	}
}

// TestInit_NewForConfigError 用非 PEM 的 CA data 让传输层构建失败：
// kubeconfig 解析成功，kubernetes.NewForConfig 在 TLS 根证书解析时报错。
func TestInit_NewForConfigError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	cfg := `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: https://127.0.0.1:1
    certificate-authority-data: bm90LWEtY2VydA==
contexts:
- name: ctx
  context:
    cluster: c
    user: u
current-context: ctx
users:
- name: u
  user: {}
`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	k := New(WithKubeConfig(path))
	if err := k.init(); err == nil {
		t.Fatal("CA data 不是 PEM 时 NewForConfig 应当报错")
	}
}

func TestInit_InClusterError(t *testing.T) {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		t.Skip("运行环境疑似集群内，InClusterConfig 可能成功")
	}
	k := New()
	if err := k.init(); err == nil {
		t.Fatal("集群外无 kubeconfig 时 init 应当报错")
	}
}

func TestLoad_InitError(t *testing.T) {
	k := New(WithKubeConfig(filepath.Join(t.TempDir(), "missing")))
	if _, err := k.Load(context.Background(), "ns/name"); err == nil {
		t.Fatal("init 失败时 Load 应当返回错误")
	}
}

func TestLoad_NamespaceMissing(t *testing.T) {
	k, _ := newFakeKube(t)
	if _, err := k.Load(context.Background(), "name"); err == nil {
		t.Fatal("namespace 为空时 Load 应当报错")
	}
}

func TestLoad_GetError(t *testing.T) {
	k, _ := newFakeKube(t)
	if _, err := k.Load(context.Background(), "ns/absent"); err == nil {
		t.Fatal("ConfigMap 不存在时 Load 应当报错")
	}
}

func TestLoad_DataSelection(t *testing.T) {
	cm := testConfigMap("ns", "name", map[string]string{"k1": "v1", "k2": "v2"})
	k, _ := newFakeKube(t, cm)

	t.Run("指定 dataKey", func(t *testing.T) {
		data, err := k.Load(context.Background(), "ns/name/k1")
		if err != nil || string(data) != "v1" {
			t.Fatalf("data=%q err=%v", data, err)
		}
	})

	t.Run("dataKey 不存在", func(t *testing.T) {
		if _, err := k.Load(context.Background(), "ns/name/absent"); err == nil {
			t.Fatal("dataKey 缺失应当报错")
		}
	})

	t.Run("合并全部 data", func(t *testing.T) {
		data, err := k.Load(context.Background(), "ns/name")
		if err != nil {
			t.Fatal(err)
		}
		if got := string(data); got != "v1\nv2" && got != "v2\nv1" {
			t.Errorf("merged = %q", got)
		}
	})

	t.Run("空 key 走默认装配", func(t *testing.T) {
		kk := &kube{
			client: k.client,
			opts:   options{Namespace: "ns", ConfigMapName: "name", DataKey: "k2"},
		}
		data, err := kk.Load(context.Background(), "")
		if err != nil || string(data) != "v2" {
			t.Fatalf("data=%q err=%v", data, err)
		}
	})
}

func TestWatchValue_InitError(t *testing.T) {
	k := New(WithKubeConfig(filepath.Join(t.TempDir(), "missing")))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := k.WatchValue(ctx, "ns/name"); err == nil {
		t.Fatal("init 失败时 WatchValue 应当返回错误")
	}
}

func TestWatchValue_NamespaceMissing(t *testing.T) {
	k, _ := newFakeKube(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := k.WatchValue(ctx, "name"); err == nil {
		t.Fatal("namespace 为空时 WatchValue 应当报错")
	}
}

func TestWatchValue_WatchError(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependWatchReactor("configmaps", func(k8stesting.Action) (bool, watch.Interface, error) {
		return true, nil, errors.New("watch broken")
	})
	k := &kube{client: cs}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := k.WatchValue(ctx, "ns/name"); err == nil {
		t.Fatal("Watch 失败时 WatchValue 应当返回错误")
	}
}

func TestWatchValue_FiltersAndDispatch(t *testing.T) {
	cm := testConfigMap("ns", "name", map[string]string{"key": "value"})
	k, fw := newFakeKube(t, cm)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ch, err := k.WatchValue(ctx, "ns/name/key")
	if err != nil {
		t.Fatal(err)
	}

	// 非 ConfigMap → 忽略；名字不匹配 → 忽略；dataKey 缺失 → 读取失败被跳过。
	fw.Modify(&v1.Pod{})
	fw.Modify(testConfigMap("ns", "other", map[string]string{"key": "x"}))
	fw.Modify(testConfigMap("ns", "name", map[string]string{"other": "x"}))
	fw.Modify(cm)

	select {
	case data := <-ch:
		if string(data) != "value" {
			t.Errorf("派发内容 = %q，期望 value", data)
		}
	case <-ctx.Done():
		t.Fatal("匹配事件未派发")
	}

	// Deleted 事件终止循环并关闭输出通道。
	fw.Delete(cm)
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("Deleted 后不应再有派发")
		}
	case <-ctx.Done():
		t.Fatal("Deleted 后输出通道未关闭")
	}
}

func TestWatchValue_NilObjectTerminates(t *testing.T) {
	k, fw := newFakeKube(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := k.WatchValue(ctx, "ns/name")
	if err != nil {
		t.Fatal(err)
	}
	fw.Modify(nil)
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("Object 为 nil 后不应再有派发")
		}
	case <-ctx.Done():
		t.Fatal("Object 为 nil 后输出通道未关闭")
	}
}

func TestWatchValue_ResultChanClosed(t *testing.T) {
	k, fw := newFakeKube(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := k.WatchValue(ctx, "ns/name")
	if err != nil {
		t.Fatal(err)
	}
	fw.Stop()
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("ResultChan 关闭后不应再有派发")
		}
	case <-ctx.Done():
		t.Fatal("ResultChan 关闭后输出通道未关闭")
	}
}

func TestWatchValue_CtxDoneIdle(t *testing.T) {
	k, _ := newFakeKube(t)
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := k.WatchValue(ctx, "ns/name")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("取消后不应再有派发")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消后输出通道未关闭")
	}
}

func TestWatchValue_CtxDoneWhileDispatching(t *testing.T) {
	cm := testConfigMap("ns", "name", map[string]string{"key": "value"})
	k, fw := newFakeKube(t, cm)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ch, err := k.WatchValue(ctx, "ns/name/key")
	if err != nil {
		t.Fatal(err)
	}

	// 首个事件填满容量 1 的缓冲且不消费，第二个事件阻塞在发送上。
	fw.Modify(cm)
	fw.Modify(cm)
	time.Sleep(50 * time.Millisecond)
	cancel()

	got := 0
	for range ch {
		got++
	}
	if got != 1 {
		t.Errorf("阻塞期间取消后收到 %d 次派发，期望 1 次", got)
	}
}
