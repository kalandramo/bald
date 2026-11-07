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
//   - 逐分支折叠：一个键下的兄弟分支各自判命中，只删被消费的那条叶子路径并清理
//     因此掏空的中间节点。拼不出字段名的分支原样留下（由严格门禁报未知键，可见
//     比猜对更有价值）；
//   - 逐段拼接的结果必须**恰好**等于某个字段名才算命中，命中即停（最短命中）；
//   - 规范键已被占用时该分支不折——同层同时出现两种形状是用户自己的歧义，留给门禁；
//   - map 字段与业务自持段整棵跳过：框架没有它们的形状知识，业务键含下划线仍然
//     表达不出来，这条限制不在本函数的能力范围内。
//
// 为什么必须逐分支：契约里同一词头下的兄弟字段是常态（`probe_live_path` 与
// `probe_ready_path`、`max_size`/`max_backups`/`max_age`、`auto_stream`/`auto_replay`）。
// 两条 env 一起注入时落树成 `probe.{live.path,ready.path}`——同一个 `probe` 节点
// 有两个子键。按「整棵子树只有一条链才折」判会两条都不折：严格门禁下报未知键，
// 非严格下 DiscardUnknown 静默丢掉，用户看到的是「env 写了却不生效」。分支之间
// 没有歧义（各自的拼接路径唯一），不该互相牵连。
func FoldEnvKeys(m map[string]any, msg proto.Message) {
	if m == nil || msg == nil {
		return
	}
	foldMessage(msg.ProtoReflect().Descriptor(), m)
}

// foldMessage 折叠一层的键，然后按字段描述符下钻。
func foldMessage(md protoreflect.MessageDescriptor, m map[string]any) {
	byName := lookupFields(md).byName

	// 两遍：先折出本层的规范键，再下钻（含刚折出来的——字段值本身可能还是多词
	// 键，只折一层等于没折）。折叠会增删键，边遍历边改是未定义行为；每遍各取
	// 快照并按字典序处理，使结果与 map 迭代序无关（同层两条链折向同一规范键时，
	// 字典序在前者赢，后者按「已被占用」留下）。
	for _, k := range foldSortedKeys(m) {
		if _, ok := byName[strings.ToLower(k)]; ok {
			continue // 平级规范名优先
		}
		foldBranches(byName, m, k)
	}

	for _, k := range foldSortedKeys(m) {
		fd, ok := byName[strings.ToLower(k)]
		if !ok {
			continue // 不是契约键：交给 [findUnknown] 判定，不在这里猜
		}
		v := m[k]

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

// foldBranches 从非规范键 k 出发折叠它的整棵子树。子树被掏空时连 k 一起删掉
// （它是 env 编码造出来的中间节点，不是用户写的键）。
func foldBranches(byName map[string]protoreflect.FieldDescriptor, m map[string]any, k string) {
	head, hv, ok := lookupKeyFold(m, k)
	if !ok {
		return
	}
	if foldWalk(byName, m, hv, strings.ToLower(head)) {
		delete(m, head)
	}
}

// foldWalk 逐段拼接键名沿 cur 下钻，命中的分支把值搬到本层规范键上并删掉叶子。
// 返回 true 表示 cur 是本次折叠掏空的中间节点，调用方应把它从父 map 删掉；仍有
// 兄弟键（或本来就是空 map / 标量）时返回 false，原样保留。
func foldWalk(byName map[string]protoreflect.FieldDescriptor, m map[string]any, cur any, joined string) bool {
	sub, ok := cur.(map[string]any)
	if !ok || len(sub) == 0 {
		return false
	}
	for _, ck := range foldSortedKeys(sub) {
		child := sub[ck]
		next := joined + "_" + strings.ToLower(ck)
		fd, hit := byName[next]
		if !hit {
			if foldWalk(byName, m, child, next) {
				delete(sub, ck)
			}
			continue
		}
		// 命中即停（最短命中）：这条分支不再往深处走，无论折成与否。
		name := string(fd.Name())
		if _, _, taken := lookupKeyFold(m, name); taken {
			continue // 规范键已由另一种形状占据
		}
		if _, _, taken := lookupKeyFold(m, fd.JSONName()); taken {
			continue
		}
		m[name] = child
		delete(sub, ck)
	}
	return len(sub) == 0
}

// foldSortedKeys 取 map 键的字典序快照（折叠增删键，不能边遍历边改）。
func foldSortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
