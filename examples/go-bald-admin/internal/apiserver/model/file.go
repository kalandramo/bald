package model

import "time"

// File 文件元数据（T5，自 go-wind-admin files 表精简移植，语义对齐源
// storage/service/v1 的 File/recordFile）。ID = 保存文件名去扩展名的 uuid
// （32 位无横线，源 FileGuid 范式）；SaveFileName 是 MinIO 对象键（含目录与
// 扩展名）。带 TenantID：文件是租户级业务数据（源 mixin TenantID），P8 自动
// 隔离。ContentHash 是上传内容的 SHA256 hex（源 ContentHash）；对象按内容
// 类型分桶（BucketName 实际落桶），LinkUrl 预签名后续迭代留空。
type File struct {
	ID            string `gorm:"primaryKey"` // uuid32（保存文件名去扩展名）
	TenantID      string `gorm:"index"`
	Provider      string // 存储提供者（"minio"）
	BucketName    string // 实际落桶（按内容类型分桶）
	SaveFileName  string // 对象键（含目录与扩展名）
	FileDirectory string // 业务目录
	FileName      string // 原始文件名
	Extension     string // 扩展名（含点，小写）
	ContentHash   string `gorm:"index"` // SHA256 hex
	Size          int64  // 字节数
	LinkUrl       string // 访问 URL（预签名后续迭代）
	MimeType      string // 嗅探出的内容类型
	CreatedBy     string // 上传者（UserID）
	CreatedAt     time.Time
	UpdatedAt     time.Time
}
