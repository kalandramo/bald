package registry

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// D15：ServiceInstance.Validate —— Register 前置校验的**单一闸门**
//
// 缺陷：四个 backend（etcd/consul/nacos/kubernetes）的 Register 都直接把
// instance.Name / instance.ID 拼进注册 key，**无任何前置校验**——缺 Name 时
// 静默成功、注册到 `<ns>//<id>` 这类畸形 key（不可按正常语义发现），缺 ID 时
// 同样静默接受。对照同组件其它入口（etcd.New 缺 endpoint 报错、
// contract.Provider 缺段报错）质量不一致。
//
// 修法：把校验收敛到契约层的 Validate()，各 backend 的 Register 首行调用——
// 单一真相源，避免「四个后端各自判断」（本项目最高频缺陷族）。
// ---------------------------------------------------------------------------

func TestValidate_OK(t *testing.T) {
	si := &ServiceInstance{ID: "i-1", Name: "svc", Endpoints: []string{"grpc://10.0.0.1:9090"}}
	if err := si.Validate(); err != nil {
		t.Fatalf("合法实例不应报错，got: %v", err)
	}
}

func TestValidate_RejectsNil(t *testing.T) {
	var si *ServiceInstance
	if err := si.Validate(); err == nil {
		t.Fatal("nil 实例必须被拒（否则 Register 会 panic 或注册空 key）")
	}
}

func TestValidate_RejectsEmptyID(t *testing.T) {
	si := &ServiceInstance{Name: "svc"}
	err := si.Validate()
	if err == nil {
		t.Fatal("缺 ID 必须被拒（否则拼出 `<ns>/<name>/` 畸形 key）")
	}
	if !strings.Contains(err.Error(), "ID") {
		t.Errorf("错误信息应指明 ID，got: %v", err)
	}
}

func TestValidate_RejectsEmptyName(t *testing.T) {
	si := &ServiceInstance{ID: "i-1"}
	err := si.Validate()
	if err == nil {
		t.Fatal("缺 Name 必须被拒（D15 的核心场景：静默注册到畸形 key）")
	}
	if !strings.Contains(err.Error(), "Name") {
		t.Errorf("错误信息应指明 Name，got: %v", err)
	}
}

// TestValidate_NilReceiverSafe 空指针调用不得 panic（返回错误即可，
// 与 Store/authz 等其它契约的 fail-closed 风格一致）。
func TestValidate_NilReceiverSafe(t *testing.T) {
	var si *ServiceInstance
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Validate 对 nil 接收者不得 panic，got: %v", r)
		}
	}()
	_ = si.Validate()
}
