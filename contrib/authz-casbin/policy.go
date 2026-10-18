package authzcasbin

import (
	"fmt"
	"strings"
)

// PolicyRow 一条角色策略行（casbin p 行：role 对 object 拥有 action）。
//
// 与 gen app 产物 internal/bootstrap/seed/policies.go 的 PolicyRow 三元组同构
// （Role/Object/Action）——生成物只产数据（默认策略集，object 走
// pkg/authz.DefaultGRPCObject 同源命名），装载行为归本桥：业务侧把生成行映射
// 成本类型后经 [NewRows] 装载。
type PolicyRow struct {
	Role   string
	Object string
	Action string
}

// Grant 一条 subject→角色绑定（casbin g 行）。
//
// p 行只声明「角色对资源的权限」；rbac_model.conf 的 matcher 是
// g(r.sub, p.sub)——subject 必须经 g 行绑定到角色才可能放行。只装策略行
// 不装绑定的授权器恒拒绝（这不是错误，是默认拒绝语义：g 行是运行期业务
// 数据——DB 用户/角色，随授权变更重载）。
type Grant struct {
	Subject string
	Role    string
}

// NewRows 用默认 RBAC 模型 + 策略行与角色绑定构造授权器——gen app/gen domain
// 生成物的策略装载入口：
//
//	rows := make([]authzcasbin.PolicyRow, 0, len(seed.Rows))
//	for _, r := range seed.Rows {
//	    rows = append(rows, authzcasbin.PolicyRow{Role: r.Role, Object: r.Object, Action: r.Action})
//	}
//	az, err := authzcasbin.NewRows(rows, grants)
//
// 语义：
//   - rows/grants 均可为 nil/空：合法输入，构造空策略 fail-closed 初始态
//     （对一切请求拒绝；等价 [New] 传空文本）。
//   - 重复行合法且幂等：同一 (role, object, action) 装载两次与一次语义相同
//     （不放大权限）。
//   - 字段含逗号/换行属非法输入（csv 行注入：一条数据裂成两行是越权面）：
//     返回错误，不静默截断。
func NewRows(rows []PolicyRow, grants []Grant) (*Authorizer, error) {
	csv, err := rowsToCSV(rows, grants)
	if err != nil {
		return nil, err
	}
	return New(csv)
}

// rowsToCSV 把策略行与角色绑定渲染为 casbin 策略文本（缺省 RBAC 模型的装载
// 格式：p, role, obj, act / g, sub, role——与 bald-admin loadPolicyCSV 等从
// DB 装载的消费方同构）。
func rowsToCSV(rows []PolicyRow, grants []Grant) (string, error) {
	var sb strings.Builder
	for _, r := range rows {
		if err := csvField("policy row", r.Role, r.Object, r.Action); err != nil {
			return "", err
		}
		fmt.Fprintf(&sb, "p, %s, %s, %s\n", r.Role, r.Object, r.Action)
	}
	for _, g := range grants {
		if err := csvField("grant", g.Subject, g.Role); err != nil {
			return "", err
		}
		fmt.Fprintf(&sb, "g, %s, %s\n", g.Subject, g.Role)
	}
	return sb.String(), nil
}

// csvField 防行注入：字段含逗号/换行会让一条数据裂成两行（越权面），拒绝。
func csvField(what string, fields ...string) error {
	for _, f := range fields {
		if strings.ContainsAny(f, ",\r\n") {
			return fmt.Errorf("casbin: %s field %q contains comma or newline", what, f)
		}
	}
	return nil
}
