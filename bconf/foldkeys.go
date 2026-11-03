package bconf

import (
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// FoldEnvKeys 按契约描述符把 env 层「下划线即路径分隔符」落成的嵌套键合回规范
// 字段名，原地修改 m。
//
// 为什么必须在**合树之前**折：`bootstrap/config` 的 environMap 把
// BALD_SERVER_HTTP_MIDDLEWARE_RATE_LIMIT_BURST 切成
// .server.http.middleware.rate.limit.burst，而契约字段是 middleware.rate_limit。
// 读侧的折叠兜底（[lookupField]/[foldPath]）发生在 unmarshal 阶段，那时合并早已
// 完成——文件层若同写了 rate_limit，「平级匹配优先」会让 env 的折叠链永远排在
// 其后，env > file 的分层承诺静默失效（不报错，只是 env 说了不算）；而严格门禁的
// [findUnknown] 没有折叠兜底，会在启动期把这条合法的注入姿势判成笔误。
// 在落树之后立刻折，两个后果一起消失：合并落在同一个规范键上，门禁看到的就是
// 契约名。
//
// 只对 env 层有意义：其余层的键名由用户直接书写（YAML/JSON 的点路径、flag 名），
// 没有「一个字符两种语义」的编码约束。文件层里写成嵌套形状的契约键仍按未知键处理，
// 那是严格门禁要抓的东西，不是要兼容的写法。
//
// 规则（与 [findUnknown] 的遍历口径一致）：
//   - 平级规范名优先：键本身就是本层字段名时按原样下钻，绝不折叠——真嵌套键不会
//     被折叠抢走；
//   - 只在「唯一链」上折：被消费的每个子节点必须只有一个键，否则切分点不唯一，
//     保持原样（由严格门禁报未知键，可见比猜对更有价值）；
//   - 逐段拼接的结果必须**恰好**等于某个字段名才算命中，命中即停（最短命中）；
//   - 规范键已被占用时不折——同层同时出现两种形状是用户自己的歧义，留给门禁；
//   - map 字段与业务自持段整棵跳过：框架没有它们的形状知识，业务键含下划线仍然
//     表达不出来，这条限制不在本函数的能力范围内。
func FoldEnvKeys(m map[string]any, msg proto.Message) {
	if m == nil || msg == nil {
		return
	}
	foldMessage(msg.ProtoReflect().Descriptor(), m)
}

// foldMessage 折叠一层的键，然后按字段描述符下钻。
func foldMessage(md protoreflect.MessageDescriptor, m map[string]any) {
	byName := lookupFields(md).byName

	// 折叠会增删键，边遍历边改是未定义行为；先取快照并按字典序处理，
	// 使结果与 map 迭代序无关。
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		key, fd := k, protoreflect.FieldDescriptor(nil)
		if f, ok := byName[strings.ToLower(k)]; ok {
			fd = f
		} else {
			var folded bool
			fd, key, folded = foldChain(byName, m, k)
			if !folded {
				continue // 不是契约键：交给 [findUnknown] 判定，不在这里猜
			}
		}
		v := m[key]

		switch {
		case fd.IsMap():
			// map 的键由业务自持，不折叠也不下钻。
		case fd.IsList():
			if child := fd.Message(); child != nil {
				items, _ := v.([]any)
				for _, it := range items {
					if im, ok := it.(map[string]any); ok {
						foldMessage(child, im)
					}
				}
			}
		case fd.Kind() == protoreflect.MessageKind:
			if sub, ok := v.(map[string]any); ok {
				foldMessage(fd.Message(), sub)
			}
		}
	}
}

// foldChain 从键 k 出发沿唯一链逐段拼接，命中本层字段名时把链尾的值搬到规范键上
// 并删除整条链；命中返回 (字段描述符, 规范键, true)。
func foldChain(byName map[string]protoreflect.FieldDescriptor, m map[string]any, k string) (protoreflect.FieldDescriptor, string, bool) {
	head, hv, ok := lookupKeyFold(m, k)
	if !ok {
		return nil, "", false
	}
	joined := strings.ToLower(head)
	cur := hv

	for {
		sub, ok := cur.(map[string]any)
		if !ok || len(sub) != 1 {
			return nil, "", false // 非唯一链：切分点不止一个，不猜
		}
		var childKey string
		for ck, cv := range sub {
			childKey, cur = ck, cv
		}
		joined += "_" + strings.ToLower(childKey)

		fd, hit := byName[joined]
		if !hit {
			continue
		}
		name := string(fd.Name())
		if _, _, taken := lookupKeyFold(m, name); taken {
			return nil, "", false // 规范键已由另一种形状占据
		}
		if _, _, taken := lookupKeyFold(m, fd.JSONName()); taken {
			return nil, "", false
		}
		delete(m, head)
		m[name] = cur
		return fd, name, true
	}
}
