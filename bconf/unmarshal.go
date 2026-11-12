package bconf

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// durationName 与 durationFullName 用于识别 google.protobuf.Duration 字段。
const (
	durationName     = "Duration"
	durationFullName = "google.protobuf.Duration"
)

// jsonBufPool 复用 UnmarshalMap 的 marshal 缓冲：契约体量固定，池化后稳态零增长。
var jsonBufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// FindUnknownKeys 找出配置树中「契约未声明」的键，返回按路径排序的列表。
//
// 用途：把「写错键名静默落到零值」这一配置系统最危险的故障模式变成**可见**。UnmarshalMap
// 的 DiscardUnknown 使同类问题在 env/文件路径上仍会静默发生。
//
// 只读：不改 m，也不改 UnmarshalMap 的行为——是否据此报错由调用方决定。
//
// allowTop 是「业务自持顶层段」白名单（如 bald-admin 的 file/login）：
//   - 命中白名单的顶层段**整棵子树跳过**（业务段形状框架无权约束）；
//   - 白名单之外，逐层比对字段描述符，任何一层出现未知键即计入。
//
// 为什么必须给白名单：框架无法区分「用户把 server.http 写成 server.htpp」
// 与「业务在 audit 段下写自持键 audit.fallback_tenant」——两者的父段都是契约段。
// 实测 bald-admin 生产配置即在 audit 段下使用 fallback_tenant（由业务
// ServerOptions 的 mapstructure 消费），故无白名单的 fail-fast 会误伤合法配置。
// 白名单的粒度是**段级**（顶层名）；契约段内部的业务键仍需调用方按需放行。
func FindUnknownKeys(m map[string]any, msg proto.Message, allowTop ...string) []string {
	if m == nil || msg == nil {
		return nil
	}
	allowed := make(map[string]bool, len(allowTop))
	for _, s := range allowTop {
		allowed[strings.ToLower(s)] = true
	}
	var out []string
	findUnknown(msg.ProtoReflect().Descriptor(), m, "", allowed, true, &out)
	sort.Strings(out)
	return out
}

// fieldLookupCache 按 message 描述符缓存「小写键名 → 字段描述符」查找表
// （proto 原名与 JSON name 两个形态）。FindUnknownKeys 的调用节奏是启动期
// 一次 + 每次配置热更重校验，每次全树扫描都经过同一批契约 message——
// 逐层重建查找表（map 分配随深度×段数放大）是其主要分配来源。
//
// 缓存键是 protoreflect.MessageDescriptor，值完全由该描述符派生：proto
// 描述符进程内不可变（compiled proto 的 filedesc 注册后只读，dynamicpb
// 的描述符同理），键与值一一对应，故无需失效机制，跨调用、跨 goroutine
// 复用安全。
var fieldLookupCache sync.Map // protoreflect.MessageDescriptor → *fieldLookup

type fieldLookup struct {
	byName map[string]protoreflect.FieldDescriptor
	// protoIdx 记录 proto 原名（精确形态，非折叠）→ 字段下标，供 canonicalize
	// 的未知键回拷判定「该键是已匹配字段的平级 proto 名」使用。
	protoIdx map[string]int32
}

// lookupFields 返回 md 的查找表，首次访问时构建。遍历顺序与键覆盖规则：
// 同序遍历字段、先小写 proto 名后小写 JSON 名，同名键后写覆盖。
func lookupFields(md protoreflect.MessageDescriptor) *fieldLookup {
	if v, ok := fieldLookupCache.Load(md); ok {
		return v.(*fieldLookup)
	}
	fields := md.Fields()
	byName := make(map[string]protoreflect.FieldDescriptor, fields.Len()*2)
	protoIdx := make(map[string]int32, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		byName[strings.ToLower(string(fd.Name()))] = fd
		byName[strings.ToLower(fd.JSONName())] = fd
		protoIdx[string(fd.Name())] = int32(i)
	}
	v := &fieldLookup{byName: byName, protoIdx: protoIdx}
	if actual, loaded := fieldLookupCache.LoadOrStore(md, v); loaded {
		return actual.(*fieldLookup) //cov:excl 并发首建「他人抢先入表」分支依赖调度，非确定性可达；行为已由 TestLookupFieldsConcurrentFirstBuild 覆盖
	}
	return v
}

// findUnknown 递归比对一层 map 与 message 的字段描述符。
//
// isTop 为 true 时，未知键先查白名单——命中则整棵子树跳过（业务段）。
// 参数是描述符而非消息实例：扫描只需要字段元信息，传 MessageDescriptor
// 免去每层递归构造 dynamicpb 消息只为取 Descriptor 的分配。
func findUnknown(md protoreflect.MessageDescriptor, m map[string]any, path string,
	allowed map[string]bool, isTop bool, out *[]string) {

	byName := lookupFields(md).byName

	for k, v := range m {
		lk := strings.ToLower(k)
		fd, ok := byName[lk]
		if !ok {
			if isTop && allowed[lk] {
				continue // 业务自持段，整棵子树放行
			}
			*out = append(*out, path+"."+k)
			continue
		}
		// 路径串推迟到实际使用处拼接（未知键收集或下钻递归）：已知标量
		// 叶键上该值从不被读取，提前拼接等于用一次分配换一个没人读的串，
		// 且两种写法产出的路径串逐字节相同。
		switch {
		case fd.IsList():
			if child := fd.Message(); child != nil {
				if items, ok := v.([]any); ok {
					for i, it := range items {
						if im, ok := it.(map[string]any); ok {
							findUnknown(child, im,
								path+"."+k+"["+strconv.Itoa(i)+"]", allowed, false, out)
						}
					}
				}
			}
		case fd.IsMap():
			// map 的值形状由用户自定义，不递归。
		case fd.Kind() == protoreflect.MessageKind:
			if sub, ok := v.(map[string]any); ok {
				findUnknown(fd.Message(), sub, path+"."+k, allowed, false, out)
			}
		}
	}
}

// UnmarshalMap 把合并后的配置树（map 形态，键为 proto 字段名）反序列化为
// Protobuf 消息。这是「proto 作为配置契约」的桥接点：配置的形状由 proto 定义，
// 由此获得编译期可查的字段名与类型，而不是依赖 mapstructure tag 的字符串匹配
// （写错键名会静默落到零值）。
//
// 流程：map → canonicalize 类型规范化（构建规范树，输入只读）→ json.Marshal → protojson.Unmarshal（解析到空消息）→ clearPresentLists → proto.Merge（合并进带默认值的 msg）。
// 注意 coerce 在 json.Marshal 之前——protojson 是严格模式，须先把 env/flag
// 来的字符串/数值规范化为 proto 可接受形态，否则反序列化报错。
//
// 用法：
//
//	cfg := bconf.NewBootstrap()              // 先填默认值
//	if err := bconf.UnmarshalMap(settings, cfg); err != nil { ... }
//
// 加载器（bootstrap/config 的层合并、bconfig KV 源、测试桩）只需先合并出 map 即可。
//
// 语义是「合并」而非「替换」：msg 中未被配置覆盖的字段保留原值（通常是
// bconf.NewBootstrap() 填入的默认值）。其中 repeated 字段表现为「替换」
// （配置中出现的列表整体覆盖默认值，而非追加，见 clearPresentLists）。
//
// 注意：proto3 的标量字段没有 presence，无法区分「未配置」与「显式配置为零值」。
// 因此若某字段的默认值是非零值（如 http.addr 默认 ":8080"），配置里显式写
// 空字符串并不会覆盖它——这是 proto3 的固有行为，不是本函数的缺陷。确实需要
// 三态语义时，请在 proto 中用 optional 关键字启用 field presence。
//
// 不修改入参：canonicalize 按 msg 的字段描述符构建一棵新的规范树，输入只读。
// 热更新回调里对同一快照的多次反序列化（契约 + 业务各一次）互不影响。
func UnmarshalMap(m map[string]any, msg proto.Message) error {
	if msg == nil {
		return fmt.Errorf("bconf: proto message is nil")
	}

	// 关键：先按 msg 的字段描述符做类型规范化，再交给 protojson。
	// 原因是 env（AutomaticEnv）与 flag（pflag 的 *Var 系列）的值在 viper 里
	// 可能是字符串或整数，而 protojson 是严格模式——"8080" 不会自动转成 int32，
	// 纳秒整数也不会自动转成 Duration 的 "10s" 字符串，直接反序列化会报错。
	settings, err := canonicalize(msg.ProtoReflect().Descriptor(), m)
	if err != nil {
		return fmt.Errorf("bconf: coerce: %w", err)
	}

	// marshal 缓冲池化：规范树 JSON 体量固定（同一契约），增长到稳态后每 op
	// 零增长；Encoder 语义与 json.Marshal 一致（HTML 转义同默认，尾部换行是
	// protojson 接受的空白）。
	buf := jsonBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	if err := json.NewEncoder(buf).Encode(settings); err != nil {
		jsonBufPool.Put(buf)
		return fmt.Errorf("bconf: marshal settings: %w", err)
	}
	data := buf.Bytes()

	// 先解析到同类型的空消息，再合并进 msg。
	// 不能直接用 protojson.Unmarshal(data, msg)：它的语义是「替换」，会先重置
	// 整个 msg，把调用方预设的默认值（conf.NewBootstrap()）一并清掉。
	parsed := msg.ProtoReflect().New().Interface()

	// DiscardUnknown：配置文件里允许出现 proto 未声明的业务配置段，
	// 不影响框架级配置的解析（proto 只约束框架级配置，见设计文档）。
	opts := protojson.UnmarshalOptions{DiscardUnknown: true}
	if err := opts.Unmarshal(data, parsed); err != nil {
		jsonBufPool.Put(buf)
		return fmt.Errorf("bconf: unmarshal into %s: %w", msg.ProtoReflect().Descriptor().FullName(), err)
	}
	jsonBufPool.Put(buf)

	// 合并前先清掉「配置中显式出现过的 list 字段」在 msg 中的旧值。
	// 原因：proto.Merge 对 repeated 字段是 append 而非替换，若不清理，
	// 默认值 ["stdout"] 与配置值 ["stdout"] 会合并成 ["stdout", "stdout"]——
	// 表现为日志向 stdout 写两遍。标量字段无此问题（后值覆盖前值）。
	clearPresentLists(msg.ProtoReflect(), parsed.ProtoReflect())

	proto.Merge(msg, parsed)
	return nil
}

// canonicalize 按 md 的字段描述符把 m 规范化为一棵新树（输入只读）：已知字段的值
// 经类型规范化后落在 JSON name 规范键下；未知键原样保留——protojson 的
// DiscardUnknown 对它们同样静默丢弃，但保留维持了「marshal 输入 = 全量树」的
// 既有口径（未知键携带不可序列化值时仍在 marshal 出口报错）。
//
// 为什么是「构建新树」而非「深拷贝后原地改写」：原地形态须先对整棵树做每节点
// 分配的深拷贝，再在字段循环里 delete+回写规范键、折叠命中时搬移+回删嵌套链，
// 每次改名/搬移都是一次 map rehash；且源树被改写后，「候选键不存在」只能靠全键
// EqualFold 扫描证明。构建形态下输入只读，缺失候选的排除降为一次首段精确查找
// （见 lookupCanonical），深拷贝的容器副本只剩规范树本身这一份。
//
// 折叠匹配依赖的描述符形状前提（同层折叠键不重合、段序列不互为严格前缀）由
// TestContractFoldShapeInvariant 钉死。
func canonicalize(md protoreflect.MessageDescriptor, m map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(m))
	fields := md.Fields()
	matched := make([]bool, fields.Len()) // 平级已消费：proto 名命中即记（等价旧 delete）
	var chainUsed map[string]struct{}     // 折叠链键，折叠命中才建（等价旧回删链）
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		raw, ok := lookupCanonical(m, matched, i, &chainUsed, string(fd.Name()), fd.JSONName())
		if !ok || raw == nil {
			continue
		}
		cv, err := canonicalValue(fd, raw)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", fd.Name(), err)
		}
		out[fd.JSONName()] = cv
		matched[i] = true
	}
	// 未被任何字段消费的键原样回拷（等价于旧「clone 后残留」形态）：已消费键
	// 不回拷，规范形写入不受源树同形键覆盖；out 已有的键也不覆盖，保持「平级
	// 匹配到的值胜出」的既有语义。proto 名集合来自 lookupFields 的描述符缓存，
	// 稳态零分配。
	protoIdx := lookupFields(md).protoIdx
	for k, v := range m {
		if i, ok := protoIdx[k]; ok && matched[i] {
			continue
		}
		if chainUsed != nil {
			if _, hit := chainUsed[k]; hit {
				continue
			}
		}
		if _, exists := out[k]; !exists {
			out[k] = v
		}
	}
	return out, nil
}

// clearPresentLists 对 src 中显式出现（长度 > 0）的 repeated 与 map 字段，
// 清空 dst 的同名字段，使随后的 proto.Merge 表现为「替换」而非「追加/逐条合并」。
//
// src 中未出现的 list/map 字段不动，其默认值得以保留。嵌套 message 递归处理。
func clearPresentLists(dst, src protoreflect.Message) {
	fields := src.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if !src.Has(fd) {
			continue // 配置中未出现，保留 dst 的默认值
		}
		switch {
		case fd.IsList():
			dst.Clear(fd)
		case fd.IsMap():
			// map 字段：proto.Merge 对 map 是逐条合并而非替换——与 repeated 的
			// 追加残留同构（默认项残留）。src 显式给出 map 时清空 dst，使其同为
			// 「替换」语义。map 字段若落入下方 MessageKind 分支（map 字段的
			// Kind 是合成 entry 消息），Mutable(fd).Message() 直接 panic——任何
			// 非空 map<string,string> 配置（如 tracer.otlp.headers、logger.
			// loki.labels）都会让 UnmarshalMap 崩溃。
			dst.Clear(fd)
		case fd.Kind() == protoreflect.MessageKind:
			// 递归处理嵌套消息（如 http.tls 下的 list 字段）。dst 缺该子消息时
			// 其嵌套 list/map 必为空、清理是 no-op，Mutable 的兜底创建纯属白付
			// 一次分配，直接跳过；Merge 会按 src 把子消息整体填出来。
			if dst.Has(fd) {
				clearPresentLists(dst.Mutable(fd).Message(), src.Get(fd).Message())
			}
		}
	}
}

// lookupCanonical 按候选键名在 map 中查找（不区分 "-" 与 "_" 的差异）。只读：
// 不改 m、不搬移折叠键——canonicalize 构建新树，消费情况记入 matched/chainUsed。
//
// 三层查找顺序与语义（逐层对应旧实现的平级 → 平级折叠 → 下钻折叠）：
//  1. 平级精确匹配（proto 原名 → JSON name）；
//  2. 平级折叠匹配：忽略大小写与 '-'/'_' 差异（kebab-case 常见，如
//     output-paths），全键扫描；
//  3. 折叠下钻：候选名含下划线（server_addrs）而配置树把它写成了嵌套形状
//     {server:{addrs:...}} 时，把「候选名的下划线段」当嵌套路径逐层下钻；
//     先精确查首段（零分配快路径），首段不存在则整链必败、免 Split 与逐层扫描。
//
// 下钻兜底留给两类输入：直接把嵌套形状喂给 UnmarshalMap 的调用方（bconf 是
// 独立库，不经 config.Store），以及没开 StrictContract 的文件/远程层里手写的
// 嵌套契约键。env 层不依赖这条：bootstrap/config 在合树之前就用 [FoldEnvKeys]
// 按契约把下划线切出的嵌套链折回规范键（§7.7）。
//
// 平级匹配优先于下钻，真嵌套键不会被折叠抢走；折叠不引入歧义的描述符形状前提
// （同层折叠键不重合、段序列不互为严格前缀）由 TestContractFoldShapeInvariant
// 钉死（旧散文前提「a_b 标量 与 a 子消息不得同层共存」是其中段的严格前缀特例）。
func lookupCanonical(m map[string]any, matched []bool, idx int, chainUsed *map[string]struct{}, names ...string) (any, bool) {
	for _, n := range names {
		if v, ok := m[n]; ok {
			matched[idx] = true
			return v, true
		}
	}
	// 平级折叠匹配。源键外层、候选内层，两侧 '-' 折叠各算一次（候选名折叠形
	// 对本调用不变，提到键循环外；定长栈缓冲承接变参候选，不逃逸零分配）。
	// 任一源键至多折叠匹配一个候选名（跨字段折叠键不重合由
	// TestContractFoldShapeInvariant 钉死；同字段两候选折叠形相同仅当候选串
	// 本身相同），遍历顺序不改变命中结果。
	var fnBuf [4]string
	foldedNames := fnBuf[:0]
	if len(names) > len(fnBuf) {
		foldedNames = make([]string, 0, len(names)) //cov:excl 调用点恒传 2 个候选（proto 名 + JSON name），超长分支不可达，防御性保留
	}
	for _, n := range names {
		foldedNames = append(foldedNames, strings.ReplaceAll(n, "-", "_"))
	}
	for key, v := range m {
		folded := strings.ReplaceAll(key, "-", "_")
		for _, n := range foldedNames {
			if strings.EqualFold(folded, n) {
				matched[idx] = true
				return v, true
			}
		}
	}
	// 折叠下钻：候选名含下划线时按段下探；首段精确查无则整链必败。
	for _, n := range names {
		if !strings.Contains(n, "_") {
			continue
		}
		if i := strings.IndexByte(n, '_'); i >= 0 {
			if _, ok := m[n[:i]]; !ok {
				continue
			}
		}
		if v, ok := foldPathRead(m, n, chainUsed); ok {
			matched[idx] = true
			return v, true
		}
	}
	return nil, false
}

// foldPathRead 把下划线候选名当嵌套路径**只读**下钻，命中返回该值并把链上键记入
// *chainUsed（canonicalize 据此不回拷这些键，等价于旧实现的「回删嵌套链」；
// 失败路径不记——旧实现失败也不删）。
//
// 要求**完整消费**全部段（max_open_connections → m[max][open][connections]），
// 中途任一段不是 map 或不存在即失败——避免把部分匹配误当命中。
func foldPathRead(m map[string]any, n string, chainUsed *map[string]struct{}) (any, bool) {
	parts := strings.Split(n, "_")

	// 逐层下钻，记录路径以便命中后记入已消费集。
	hops := make([]string, 0, len(parts))
	cur := m
	for i, p := range parts {
		k, v, ok := lookupKeyFold(cur, p)
		if !ok {
			return nil, false
		}
		hops = append(hops, k)
		if i == len(parts)-1 {
			// 完整命中：链上键全部记入已消费集。
			if *chainUsed == nil {
				*chainUsed = make(map[string]struct{}, len(parts))
			}
			for _, hk := range hops {
				(*chainUsed)[hk] = struct{}{}
			}
			return v, true
		}
		next, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return nil, false //cov:excl 末段迭代内必 return（命中/查无/非 map 之一），空 parts 不进入本函数，循环不可能正常退出
}

// lookupKeyFold 在单层 map 中按键名查找（先精确，再忽略大小写与 "-"/"_" 差异）。
func lookupKeyFold(m map[string]any, key string) (string, any, bool) {
	if v, ok := m[key]; ok {
		return key, v, true
	}
	for k, v := range m {
		if strings.EqualFold(strings.ReplaceAll(k, "-", "_"), strings.ReplaceAll(key, "-", "_")) {
			return k, v, true
		}
	}
	return "", nil, false
}

// canonicalValue 按字段描述符把单个值转换成 protojson 可接受的类型。
func canonicalValue(fd protoreflect.FieldDescriptor, raw any) (any, error) {
	// repeated 字段：逐元素转换。
	if fd.IsList() && !fd.IsMap() {
		items, ok := toSlice(raw)
		if !ok {
			// 标量单值也接受（如 output-paths: stdout），包成单元素切片。
			items = []any{raw}
		}
		out := make([]any, 0, len(items))
		for i, it := range items {
			v, err := coerceScalar(fd, it)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			out = append(out, v)
		}
		return out, nil
	}

	// map 字段：只转换 value。
	if fd.IsMap() {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("expect map, got %T", raw)
		}
		out := make(map[string]any, len(m))
		for k, v := range m {
			cv, err := coerceScalar(fd.MapValue(), v)
			if err != nil {
				return nil, fmt.Errorf("map value %q: %w", k, err)
			}
			out[k] = cv
		}
		return out, nil
	}

	// 消息字段：Duration 需要特殊处理，其余递归构建规范子树。
	if fd.Kind() == protoreflect.MessageKind {
		if isDuration(fd.Message()) {
			return coerceDuration(raw)
		}
		sub, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("expect object for message field, got %T", raw)
		}
		return canonicalize(fd.Message(), sub)
	}

	return coerceScalar(fd, raw)
}

// coerceScalar 转换标量：解决 env / flag 值与 proto 类型不匹配的问题。
func coerceScalar(fd protoreflect.FieldDescriptor, raw any) (any, error) {
	if fd.Kind() == protoreflect.EnumKind {
		return coerceEnum(fd, raw)
	}

	// 已经是正确类型（来自本地 yaml/json 文件）则原样返回。
	switch fd.Kind() {
	case protoreflect.StringKind:
		// 数字/布尔转字符串，兼容 flag 与 env 的写法。
		switch v := raw.(type) {
		case string:
			return v, nil
		case bool:
			return strconv.FormatBool(v), nil
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return fmt.Sprint(v), nil
		case float32, float64:
			return strconv.FormatFloat(toFloat(v), 'f', -1, 64), nil
		}
		return nil, fmt.Errorf("expect string, got %T", raw)

	case protoreflect.BoolKind:
		switch v := raw.(type) {
		case bool:
			return v, nil
		case string:
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, fmt.Errorf("expect bool, got %q", v)
			}
			return b, nil
		}
		return nil, fmt.Errorf("expect bool, got %T", raw)

	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return coerceInt(raw, fd.Kind())

	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return coerceUint(raw)

	case protoreflect.FloatKind, protoreflect.DoubleKind:
		switch v := raw.(type) {
		case float32, float64:
			return toFloat(v), nil
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			n, err := anyToInt64(v)
			if err != nil {
				return nil, err //cov:excl v 已由 case 标签限定为整型，anyToInt64 对整型恒不报错
			}
			return float64(n), nil
		case string:
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return nil, fmt.Errorf("expect number, got %q", v)
			}
			return f, nil
		}
		return nil, fmt.Errorf("expect number, got %T", raw)

	case protoreflect.BytesKind:
		if s, ok := raw.(string); ok {
			return s, nil // protojson 期望 base64 字符串
		}
		return nil, fmt.Errorf("expect base64 string for bytes, got %T", raw)
	}

	return raw, nil
}

// coerceInt 转换成有符号整数。字符串（env 常见）与 Duration 都支持。
func coerceInt(raw any, kind protoreflect.Kind) (any, error) {
	bits := 64
	if kind == protoreflect.Int32Kind || kind == protoreflect.Sint32Kind || kind == protoreflect.Sfixed32Kind {
		bits = 32
	}
	switch v := raw.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		n, err := anyToInt64(v)
		if err != nil {
			return nil, err //cov:excl v 已由 case 标签限定为整型，anyToInt64 对整型恒不报错
		}
		if bits == 32 && (n > math.MaxInt32 || n < math.MinInt32) {
			return nil, fmt.Errorf("value %d overflows int32", n)
		}
		return n, nil
	case float32, float64:
		f := toFloat(v)
		if f != math.Trunc(f) {
			return nil, fmt.Errorf("expect integer, got %v", f)
		}
		// float64 精度高于 int64：超界值 int64(f) 会静默回绕，须显式拒绝。
		// 边界用 2^63 精确常量（float64(math.MaxInt64) 恰为 2^63，">" 会放过它）。
		if f >= 1<<63 || f < math.MinInt64 {
			return nil, fmt.Errorf("value %v overflows int64", f)
		}
		return int64(f), nil
	case string:
		n, err := strconv.ParseInt(v, 10, bits)
		if err != nil {
			return nil, fmt.Errorf("expect integer, got %q", v)
		}
		return n, nil
	case time.Duration:
		return int64(v), nil
	}
	return nil, fmt.Errorf("expect integer, got %T", raw)
}

// coerceUint 转换成无符号整数。
func coerceUint(raw any) (any, error) {
	switch v := raw.(type) {
	case uint, uint8, uint16, uint32, uint64:
		return v, nil
	case int, int8, int16, int32, int64:
		n, err := anyToInt64(v)
		if err != nil {
			return nil, err //cov:excl v 已由 case 标签限定为整型，anyToInt64 对整型恒不报错
		}
		if n < 0 {
			return nil, fmt.Errorf("expect unsigned integer, got %d", n)
		}
		return uint64(n), nil
	case string:
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("expect unsigned integer, got %q", v)
		}
		return n, nil
	case float32, float64:
		// JSON 解码后数字为 float64：整数值应放行（与 coerceInt 对称），
		// 否则 JSON 配置文档中的 uint 字段必然报错。
		f := toFloat(v)
		if f != math.Trunc(f) {
			return nil, fmt.Errorf("expect unsigned integer, got %v", f)
		}
		if f < 0 {
			return nil, fmt.Errorf("expect unsigned integer, got %v", f)
		}
		// 边界用 2^64 精确常量：float64(math.MaxUint64) 会舍入到 2^64。
		if f >= 1<<64 {
			return nil, fmt.Errorf("value %v overflows uint64", f)
		}
		return uint64(f), nil
	}
	return nil, fmt.Errorf("expect unsigned integer, got %T", raw)
}

// coerceEnum 转换枚举：接受枚举值名（"json"）与数字（0）。
func coerceEnum(fd protoreflect.FieldDescriptor, raw any) (any, error) {
	values := fd.Enum().Values()
	switch v := raw.(type) {
	case string:
		// 已是合法枚举名则直接返回（大小写不敏感由 protojson 处理）。
		if values.ByName(protoreflect.Name(v)) != nil {
			return v, nil
		}
		// 数字字符串按编号解析。
		if n, err := strconv.ParseInt(v, 10, 32); err == nil {
			if values.ByNumber(protoreflect.EnumNumber(n)) != nil {
				return n, nil
			}
		}
		names := make([]string, 0, values.Len())
		for i := 0; i < values.Len(); i++ {
			names = append(names, string(values.Get(i).Name()))
		}
		return nil, fmt.Errorf("invalid enum value %q, want one of %v", v, names)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		n, err := anyToInt64(v)
		if err != nil {
			return nil, err //cov:excl v 已由 case 标签限定为整型，anyToInt64 对整型恒不报错
		}
		if values.ByNumber(protoreflect.EnumNumber(n)) != nil {
			return n, nil
		}
		return nil, fmt.Errorf("invalid enum number %d", n)
	}
	return nil, fmt.Errorf("expect enum name or number, got %T", raw)
}

// coerceDuration 把配置值转成 protojson 能接受的 Duration 字符串。
//
// 三种来源形态：
//  1. 本地 yaml/json 文件："10s"、"1.5s"、"1500ms"、"1m30s"；
//  2. env（AutomaticEnv）：同上的字符串；
//  3. flag（pflag.DurationVar）：time.Duration 或纳秒整数。
//
// 字符串分支同样要过 formatDuration：protojson 的 Duration 只接受以 s 结尾的
// 十进制秒数，"1500ms" 与 "1m30s" 是合法 Go 时长串却不是合法 protojson 值。早先
// 这里只用 time.ParseDuration 校验后原样返回，于是「校验通过」的串仍会在下一步
// 报错，且报的是不带键名的 protojson 位置（实测 `initial: 1500ms` 红在
// `line 1:60`）。解析后重排成 "<seconds>s" 让四种写法都能落进同一个字段，
// 也让那句「提前暴露错误」成立。
func coerceDuration(raw any) (any, error) {
	switch v := raw.(type) {
	case string:
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid duration %q: %w", v, err)
		}
		return formatDuration(d), nil
	case time.Duration:
		return formatDuration(v), nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		n, err := anyToInt64(v)
		if err != nil {
			return nil, err //cov:excl v 已由 case 标签限定为整型，anyToInt64 对整型恒不报错
		}
		return formatDuration(time.Duration(n)), nil
	case float32, float64:
		return formatDuration(time.Duration(toFloat(v))), nil
	}
	return nil, fmt.Errorf("expect duration (e.g. \"10s\"), got %T", raw)
}

// formatDuration 把 time.Duration 格式化为 protojson Duration 串（十进制秒 + "s"）。
func formatDuration(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s"
}

// isDuration 判断消息描述符是否为 google.protobuf.Duration。
func isDuration(md protoreflect.MessageDescriptor) bool {
	return md != nil && (md.FullName() == durationFullName ||
		(md.Name() == durationName && md.ParentFile().Package() == "google.protobuf"))
}

// toSlice 尝试把值转成 []any，支持反射之外的常见容器类型。
func toSlice(raw any) ([]any, bool) {
	switch v := raw.(type) {
	case []any:
		return v, true
	case []string:
		out := make([]any, 0, len(v))
		for _, s := range v {
			out = append(out, s)
		}
		return out, true
	case []int:
		out := make([]any, 0, len(v))
		for _, n := range v {
			out = append(out, n)
		}
		return out, true
	}
	return nil, false
}

// anyToInt64 把任意整数类型转为 int64。
func anyToInt64(v any) (int64, error) {
	switch n := v.(type) {
	case int:
		return int64(n), nil
	case int8:
		return int64(n), nil
	case int16:
		return int64(n), nil
	case int32:
		return int64(n), nil
	case int64:
		return n, nil
	case uint:
		return int64(n), nil
	case uint8:
		return int64(n), nil
	case uint16:
		return int64(n), nil
	case uint32:
		return int64(n), nil
	case uint64:
		return int64(n), nil
	}
	return 0, fmt.Errorf("not an integer: %T", v)
}

// toFloat 把 float32/float64 归一为 float64。
func toFloat(v any) float64 {
	switch f := v.(type) {
	case float32:
		return float64(f)
	case float64:
		return f
	}
	return 0
}
