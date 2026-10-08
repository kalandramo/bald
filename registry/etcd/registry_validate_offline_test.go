package etcd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kalandramo/bald/registry"
)

// TestRegister_RejectsInvalidInstanceBeforeNetwork 是 D15 的**离线**接线验证。
//
// 为什么单独存在：既有的 TestRegister_MissingNameIsRejected 走 newTestRegistry，
// 需要真实 etcd（:2379），环境缺失即 SKIP——于是「etcd 后端确实接了 Validate
// 闸门」这一点在无 etcd 的环境里从未被断言过（vet 只保证能编译，不保证调用点
// 存在）。本测试用**不可达**端点构造 registry：
//
//   - clientv3.New 是惰性建连（不在 New 时触网，见 newTestRegistry 注释）；
//   - Register 的 Validate 闸门是第一行，在任何网络动作之前返回。
//
// 故：若闸门被误删/被移到网络动作之后，本测试会失败（超时或返回网络错误而非
// 校验错误）；闸门在位则快速返回校验错误。无需 etcd，恒运行。
func TestRegister_RejectsInvalidInstanceBeforeNetwork(t *testing.T) {
	// 127.0.0.1:1 是不可达端口——确保任何网络活动都会失败/阻塞，从而证明
	// 我们观察到的错误来自校验闸门而非网络。
	r, err := New(
		WithEndpoints("127.0.0.1:1"),
		WithDialTimeout(200*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("New（惰性建连）不应失败: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 缺 Name：必须在触网前被拒。
	start := time.Now()
	err = r.Register(ctx, &registry.ServiceInstance{ID: "id-only"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("D15：etcd.Register 缺 Name 必须返回错误（而非静默注册到 `<ns>//<id>` 畸形 key）")
	}
	if !strings.Contains(err.Error(), "Name") {
		t.Errorf("应为校验错误（提及 Name），got: %v", err)
	}
	// 闸门在任何网络动作之前 → 不应等到 dial 超时。放宽到远小于 dialTimeout
	// 的阈值，确保不是「网络失败被误当校验错误」。
	if elapsed > 150*time.Millisecond {
		t.Errorf("校验闸门应在触网前返回，耗时 %v 接近/超过 dial 超时（疑闸门被移到网络动作之后）", elapsed)
	}

	// 缺 ID 同样被拒。
	if err := r.Register(ctx, &registry.ServiceInstance{Name: "svc"}); err == nil {
		t.Error("D15：缺 ID 也必须被拒")
	}

	// 反例对照：合法实例**会**越过闸门（进入网络路径）——用不可达端点则报
	// 网络类错误，而非校验错误。证明闸门不是「一律拒绝」。
	networkErr := r.Register(ctx, &registry.ServiceInstance{Name: "svc", ID: "id"})
	if networkErr == nil {
		t.Fatal("合法实例对不可达端点应失败（网络错误），不应成功")
	}
	if strings.Contains(networkErr.Error(), "Name is required") {
		t.Errorf("合法实例不应被校验闸门拦下，got: %v", networkErr)
	}
}
