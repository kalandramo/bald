package kubernetes

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// TestKube 集成测试：需要本地 kubeconfig 与可达集群。
// 默认跳过，显式设置 BALD_KUBE_INTEGRATION=1 才执行（否则无集群环境下
// go test ./... 无法保持全绿）；环境不具备时亦跳过而非 panic
// （此前 err 后继续执行导致 nil deref panic）。
func TestKube(t *testing.T) {
	if os.Getenv("BALD_KUBE_INTEGRATION") == "" {
		t.Skip("set BALD_KUBE_INTEGRATION=1 to run kubernetes integration test (requires reachable cluster)")
	}
	home := homedir.HomeDir()
	if home == "" {
		t.Skip("home directory not found, skip kubernetes integration test")
	}
	kubeconfig := filepath.Join(home, ".kube", "config")
	if _, err := os.Stat(kubeconfig); err != nil {
		t.Skipf("kubeconfig %s not found, skip kubernetes integration test", kubeconfig)
	}
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("build config: %v", err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	cmWatcher, err := client.CoreV1().ConfigMaps("mesh").Watch(context.Background(), metav1.ListOptions{
		LabelSelector: "app=test",
		// WithFieldSelector:        "",
	})
	if err != nil {
		t.Fatalf("watch configmaps: %v", err)
	}
	go func() {
		time.Sleep(5 * time.Second)
		cmWatcher.Stop()
	}()
	for c := range cmWatcher.ResultChan() {
		if c.Object == nil {
			return
		}
		t.Log(c.Type, c.Object)
	}
}
