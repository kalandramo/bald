package appkit

import "fmt"

// Ambient 是一条「计划之外、由构建环境注入」的接线说明：build tag 分叉、
// init() 注册回调、外部代码直接改写全局钩子等。它不产生节点——内部对计划
// 不可分析——但必须可见：Compile 把每条 Ambient 投影为 Plan.Warnings 的
// ambient 诊断（I10），--plan 与 doctor 随计划打印。
//
// 短期形态（设计 §8.2 Q7）：应用把已知的环境注入**显式声明**出来，逃逸舱
// 从「只存在于构建脚本」变成「计划里的一行」。长期由 AppSpec `variants`
// 让分叉本身进声明，届时 Ambient 退化为迁移期接口。
type Ambient struct {
	// Name 是稳定身份（诊断的 Subject），如 "build-tag:grpcgw"。
	Name string
	// Detail 是人读说明：注入了什么、由什么条件触发。
	Detail string
}

// WithAmbient 声明环境注入的接线（可多次调用，追加；重名去重）。
// 重名且 Detail 一致视为同一逃逸舱，只产一条诊断；重名而 Detail 冲突在
// 计划期报错——同一身份无法同时描述两种注入，静默择一会让计划说谎。
// Name 为空同样报错——无身份的逃逸舱无法在计划里被引用。
func WithAmbient(notes ...Ambient) Option {
	return optionFunc(func(d *Decl) { d.Ambient = append(d.Ambient, notes...) })
}

// ambientWarnings 把环境注入的接线显式化；顺序无关由 Plan 收尾统一排序去重。
// 重名裁决在此完成（顺序无关）：同 Detail 合并，异 Detail 报错。
func ambientWarnings(notes []Ambient) ([]Diagnostic, error) {
	if len(notes) == 0 {
		return nil, nil
	}
	byName := make(map[string]string, len(notes))
	for _, note := range notes {
		if note.Name == "" {
			return nil, fmt.Errorf("appkit: ambient declaration requires a Name")
		}
		msg := note.Detail
		if msg == "" {
			msg = "wiring injected by the build environment is not described by the plan"
		}
		if prev, ok := byName[note.Name]; ok {
			if prev != msg {
				return nil, fmt.Errorf("appkit: ambient %q declared twice with conflicting Detail", note.Name)
			}
			continue
		}
		byName[note.Name] = msg
	}
	out := make([]Diagnostic, 0, len(byName))
	for _, note := range notes {
		msg, ok := byName[note.Name]
		if !ok {
			continue
		}
		delete(byName, note.Name)
		out = append(out, Diagnostic{
			Level:   DiagWarn,
			Code:    "ambient",
			Subject: note.Name,
			Message: msg,
		})
	}
	return out, nil
}
