package codegen

// 本文件集中生成期报错的文档出口（DX-6）：Gate1（schema 门）与 Gate2
// （契约门）的概念名词在报错里首次出现处挂 docs 锚点，用户不必搜索。
// 路径相对仓库根；docscheck 只校验目标文件存在，不校验锚点 slug。

const (
	docsHintGate = "see: docs/guide/zh-CN/用 bald CLI 起步新服务.md#41-三道门"
	docsHintSpec = "see: docs/guide/zh-CN/用 bald CLI 起步新服务.md#31-字段参考json-字段名即-protojson-形式"
)
