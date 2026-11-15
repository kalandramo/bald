package codegen

// well_known_types.go：proto 的知名类型（google.protobuf.*）不属于域包，绑定
// 生成必须把它们折算到各自的 Go 包。
//
// 之前这里是个真 bug：normalizeProtoType 只认「去 proto 包前缀 + 嵌套点换下划线」，
// 于是 `google.protobuf.Empty` 变成 `google_protobuf_Empty`，再被拼上域包别名，
// 得到 `postv1.google_protobuf_Empty`——一个不存在的标识符。而 gen proto 的 CRUD
// 模板里 Delete 返回的正是 google.protobuf.Empty，所以「生成一个域再派生绑定」这条
// 最主流的路径必然编译失败。修法不是特判 Empty，而是把「类型在哪个包」这件事
// 说清楚：知名类型有自己的 import 与别名。

// wellKnownType 是一个 proto 知名类型的 Go 落点。
type wellKnownType struct {
	Import string // Go 包路径
	Alias  string // 生成文件里的 import 别名
	Type   string // 包内类型名
}

// wellKnownTypes 覆盖 google/protobuf 下会被业务契约直接引用的那批类型。
// 未列出的知名类型不猜：落到域包前缀上，编译期立刻暴露（比默默生成错名好）。
// wrapperspb 家族是完整的八个——漏一个就生成 `域包.google_protobuf_Int32Value`
// 这种双重非法的未导出标识符（对抗审查 adv-codegen Bug2 实测 Int32Value）。
var wellKnownTypes = map[string]wellKnownType{
	"google.protobuf.Empty":       {Import: "google.golang.org/protobuf/types/known/emptypb", Alias: "emptypb", Type: "Empty"},
	"google.protobuf.Timestamp":   {Import: "google.golang.org/protobuf/types/known/timestamppb", Alias: "timestamppb", Type: "Timestamp"},
	"google.protobuf.Duration":    {Import: "google.golang.org/protobuf/types/known/durationpb", Alias: "durationpb", Type: "Duration"},
	"google.protobuf.Any":         {Import: "google.golang.org/protobuf/types/known/anypb", Alias: "anypb", Type: "Any"},
	"google.protobuf.Struct":      {Import: "google.golang.org/protobuf/types/known/structpb", Alias: "structpb", Type: "Struct"},
	"google.protobuf.FieldMask":   {Import: "google.golang.org/protobuf/types/known/fieldmaskpb", Alias: "fieldmaskpb", Type: "FieldMask"},
	"google.protobuf.Value":       {Import: "google.golang.org/protobuf/types/known/structpb", Alias: "structpb", Type: "Value"},
	"google.protobuf.ListValue":   {Import: "google.golang.org/protobuf/types/known/structpb", Alias: "structpb", Type: "ListValue"},
	"google.protobuf.NullValue":   {Import: "google.golang.org/protobuf/types/known/structpb", Alias: "structpb", Type: "NullValue"},
	"google.protobuf.DoubleValue": {Import: "google.golang.org/protobuf/types/known/wrapperspb", Alias: "wrapperspb", Type: "DoubleValue"},
	"google.protobuf.FloatValue":  {Import: "google.golang.org/protobuf/types/known/wrapperspb", Alias: "wrapperspb", Type: "FloatValue"},
	"google.protobuf.Int64Value":  {Import: "google.golang.org/protobuf/types/known/wrapperspb", Alias: "wrapperspb", Type: "Int64Value"},
	"google.protobuf.UInt64Value": {Import: "google.golang.org/protobuf/types/known/wrapperspb", Alias: "wrapperspb", Type: "UInt64Value"},
	"google.protobuf.Int32Value":  {Import: "google.golang.org/protobuf/types/known/wrapperspb", Alias: "wrapperspb", Type: "Int32Value"},
	"google.protobuf.UInt32Value": {Import: "google.golang.org/protobuf/types/known/wrapperspb", Alias: "wrapperspb", Type: "UInt32Value"},
	"google.protobuf.BoolValue":   {Import: "google.golang.org/protobuf/types/known/wrapperspb", Alias: "wrapperspb", Type: "BoolValue"},
	"google.protobuf.StringValue": {Import: "google.golang.org/protobuf/types/known/wrapperspb", Alias: "wrapperspb", Type: "StringValue"},
	"google.protobuf.BytesValue":  {Import: "google.golang.org/protobuf/types/known/wrapperspb", Alias: "wrapperspb", Type: "BytesValue"},
}

// goTypeExpr 把 proto 类型名折算成生成代码里的类型表达式，并返回需要的额外
// import（域内类型返回 nil——它走 service 自己的 go_package）。
func goTypeExpr(protoPkg, goAlias, protoType string) (string, *bindImport) {
	if wk, ok := wellKnownTypes[protoType]; ok {
		return wk.Alias + "." + wk.Type, &bindImport{Alias: wk.Alias, Path: wk.Import}
	}
	return goAlias + "." + normalizeProtoType(protoPkg, protoType), nil
}
