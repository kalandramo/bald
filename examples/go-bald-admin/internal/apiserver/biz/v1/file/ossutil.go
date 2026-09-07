// Package file 文件模块业务层（T5，自 go-wind-admin storage 域精简移植）。
//
// ossutil.go 自源项目 go-wind-admin/backend/pkg/oss 精简移植：只保留上传链路
// 用到的常量与工具，语义对齐源 constants.go / utils.go。差异点：
//   - 文件名生成只保留 UUID 策略（源 GenerateFileNameType 多态裁剪，D4）；
//   - UUID 用 google/uuid（bald 生态不依赖 tx7do/go-utils）；
//   - URL 类工具（JoinObjectUrl/ReplaceEndpointHost）与 SetDownloadRange
//     随预签名功能后续迭代再移植。
package file

import (
	"bytes"
	"fmt"
	"mime"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// MaxUploadSize 单次直传文件最大字节数（源 MaxUploadSize）。
const MaxUploadSize int64 = 50 << 20 // 50 MiB

// 按内容类型分桶名（源 Bucket* 常量）。file.bucket 配置可覆盖 files 兜底桶。
const (
	BucketImages = "images"
	BucketVideos = "videos"
	BucketAudios = "audios"
	BucketDocs   = "docs"
	BucketFiles  = "files"
)

// allowedMimePrefixes / allowedExactMimeTypes：源 AllowedMimePrefixes /
// AllowedExactMimeTypes。嗅探真实文件内容得到 MIME 后，需命中其中之一才允许
// 上传（防扩展名伪装；可执行/HTML 等危险类型天然被排除在外）。
var allowedMimePrefixes = []string{
	"image/", // png / jpg / gif / webp / bmp ...
	"video/", // mp4 / webm / quicktime ...
	"audio/", // mpeg / wav / ogg ...
}

var allowedExactMimeTypes = map[string]struct{}{
	"text/plain":       {},
	"text/markdown":    {},
	"application/json": {},
	"application/pdf":  {},
	"application/zip":  {},
	"application/gzip": {},

	"application/x-gzip":           {},
	"application/x-tar":            {},
	"application/x-7z-compressed":  {},
	"application/x-rar-compressed": {},

	// Office 文档
	"application/msword": {},
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": {},
	"application/vnd.ms-excel": {},
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         {},
	"application/vnd.ms-powerpoint":                                             {},
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": {},
}

// IsAllowedMimeType 判断某 MIME 类型是否在上传白名单内（源同名函数）。
// 同时支持前缀（image/* 等）与精确匹配（pdf/zip 等）。
func IsAllowedMimeType(mimeType string) bool {
	if mimeType == "" {
		return false
	}
	// http.DetectContentType 输出带参数（如 "text/plain; charset=utf-8"），
	// 精确匹配前必须剥参数，否则 text/* 与 application/json 等无魔术头类型
	// 永不命中白名单（T8 §9 真调暴露的 e2e 盲区：t5 只覆盖 PNG/exe 两极）。
	if mt, _, err := mime.ParseMediaType(mimeType); err == nil {
		mimeType = mt
	}
	if _, ok := allowedExactMimeTypes[mimeType]; ok {
		return true
	}
	for _, prefix := range allowedMimePrefixes {
		if len(mimeType) >= len(prefix) && mimeType[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// IsFileDirectorySafe 校验客户端传入的 FileDirectory 是否安全（源同名函数）。
// 仅允许字母、数字、下划线、连字符与斜杠；拒绝 ..、绝对路径等穿越/注入手法。
func IsFileDirectorySafe(dir string) bool {
	if dir == "" {
		return true // 空目录允许（落到根命名空间）
	}
	for _, r := range dir {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_', r == '-', r == '/':
			// 合法字符
		default:
			return false
		}
	}
	if strings.Contains(dir, "..") { // 拒绝路径穿越段
		return false
	}
	if strings.HasPrefix(dir, "/") { // 拒绝绝对路径样式
		return false
	}
	return true
}

// ContentTypeToBucketName 根据内容类型获取存储桶名称（源同名函数）：
// image/video/audio → 同名桶；text 与常见文档 → docs；其余 → files。
func ContentTypeToBucketName(contentType string) string {
	if strings.TrimSpace(contentType) == "" {
		return BucketFiles
	}

	// 解析 media type，忽略参数（如 charset）
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mt = strings.ToLower(strings.TrimSpace(contentType))
	} else {
		mt = strings.ToLower(mt)
	}

	parts := strings.SplitN(mt, "/", 2)
	if len(parts) != 2 {
		return BucketFiles
	}
	main, sub := parts[0], parts[1]

	switch main {
	case "image":
		return BucketImages
	case "video":
		return BucketVideos
	case "audio":
		return BucketAudios
	case "text":
		return BucketDocs
	case "application":
		// 常见文档/办公类型映射到 docs，其余为 files
		switch sub {
		case "pdf", "json":
			return BucketDocs
		default:
			if strings.HasPrefix(sub, "vnd.ms-") ||
				strings.Contains(sub, "officedocument") ||
				strings.Contains(sub, "word") ||
				strings.Contains(sub, "excel") ||
				strings.Contains(sub, "powerpoint") {
				return BucketDocs
			}
			return BucketFiles
		}
	default:
		return BucketFiles
	}
}

// ContentTypeToFileExtension 根据内容类型获取文件后缀（含前导点，小写）。
// 精简自源同名函数：常用映射 + 标准库回退。
func ContentTypeToFileExtension(contentType string) string {
	if strings.TrimSpace(contentType) == "" {
		return ""
	}

	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mt = strings.ToLower(strings.TrimSpace(contentType))
	} else {
		mt = strings.ToLower(mt)
	}

	switch mt {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	case "image/x-icon", "image/vnd.microsoft.icon":
		return ".ico"
	case "image/svg+xml":
		return ".svg"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "video/quicktime":
		return ".mov"
	case "video/x-matroska", "video/mkv":
		return ".mkv"
	case "audio/mpeg":
		return ".mp3"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "audio/ogg", "audio/vorbis":
		return ".ogg"
	case "audio/mp4":
		return ".m4a"
	case "text/plain":
		return ".txt"
	case "text/html":
		return ".html"
	case "text/css":
		return ".css"
	case "text/csv":
		return ".csv"
	case "text/xml":
		return ".xml"
	case "text/markdown":
		return ".md"
	case "text/javascript", "application/javascript", "application/x-javascript":
		return ".js"
	case "application/pdf":
		return ".pdf"
	case "application/json":
		return ".json"
	case "application/zip":
		return ".zip"
	case "application/x-tar":
		return ".tar"
	case "application/gzip", "application/x-gzip":
		return ".gz"
	case "application/x-7z-compressed", "application/7z":
		return ".7z"
	case "application/msword":
		return ".doc"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return ".docx"
	case "application/vnd.ms-excel":
		return ".xls"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return ".xlsx"
	case "application/vnd.ms-powerpoint":
		return ".ppt"
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return ".pptx"
	case "application/octet-stream":
		return ".bin"
	}

	// 回退：标准库尝试获取扩展名
	exts, _ := mime.ExtensionsByType(mt)
	if len(exts) > 0 {
		ext := exts[0]
		if !strings.HasPrefix(ext, ".") {
			return "." + ext
		}
		return ext
	}
	return "" // 未知类型返回空串，调用方兜底 "bin"
}

// DetectFileType 根据文件内容推断 MIME 类型和扩展名（源同名函数）：
// 前 512 字节 http.DetectContentType + 常见魔术头优先判定。
// 输出 mimeType（如 "image/png"）和 ext（如 ".png"，未知则为空）。
func DetectFileType(fileContent []byte) (mimeType, ext string) {
	head := fileContent
	if len(head) > 512 {
		head = head[:512]
	}
	mimeType = http.DetectContentType(head)

	// 常见魔术头优先判定
	switch {
	case len(fileContent) >= 8 && bytes.Equal(fileContent[:8], []byte("\x89PNG\r\n\x1a\n")):
		return "image/png", ".png"
	case len(fileContent) >= 3 && bytes.Equal(fileContent[:3], []byte{0xff, 0xd8, 0xff}):
		// JPG 可以以 FF D8 FF 开头
		return "image/jpeg", ".jpg"
	case len(fileContent) >= 6 && (bytes.Equal(fileContent[:6], []byte("GIF87a")) || bytes.Equal(fileContent[:6], []byte("GIF89a"))):
		return "image/gif", ".gif"
	case len(fileContent) >= 5 && bytes.Equal(fileContent[:5], []byte("%PDF-")):
		return "application/pdf", ".pdf"
	case len(fileContent) >= 4 && bytes.Equal(fileContent[:4], []byte("PK\x03\x04")):
		// zip / docx / xlsx / jar 等基于 zip 的格式，默认返回 .zip
		return "application/zip", ".zip"
	case len(fileContent) >= 12 && bytes.Equal(fileContent[4:8], []byte("ftyp")):
		// ftyp box 常见于 MP4/ISO media files；更细分需要读取 brand 字段
		return "video/mp4", ".mp4"
	case len(fileContent) >= 3 && bytes.Equal(fileContent[:3], []byte("ID3")):
		return "audio/mpeg", ".mp3"
	case len(fileContent) >= 2 && fileContent[0] == 0xFF && (fileContent[1]&0xE0) == 0xE0:
		// 另一种 MP3 帧头判定（0xFF Ex）
		return "audio/mpeg", ".mp3"
	case len(fileContent) >= 12 && bytes.Equal(fileContent[:4], []byte("RIFF")) && bytes.Equal(fileContent[8:12], []byte("WAVE")):
		return "audio/wav", ".wav"
	case len(fileContent) >= 2 && bytes.Equal(fileContent[:2], []byte("BM")):
		return "image/bmp", ".bmp"
	}

	// 魔术头未命中：从 http.DetectContentType 的 MIME 类型获取扩展名
	if mimeType != "" {
		exts, _ := mime.ExtensionsByType(mimeType)
		if len(exts) > 0 {
			return mimeType, exts[0]
		}
	}
	// 未知扩展，返回检测到的 mimeType（ext 为空）
	return mimeType, ""
}

// GeneraUUIDFileName 生成基于 UUID 的文件名（源 GeneraUUIDFileName，语义保持；
// google/uuid 替代 tx7do id.NewGUIDv4(false)）：去横线 UUID + 清理后的扩展名。
func GeneraUUIDFileName(fileExt string) string {
	name := strings.ReplaceAll(uuid.NewString(), "-", "")
	cleanExt := strings.TrimPrefix(fileExt, ".")
	if cleanExt == "" {
		return name
	}
	return fmt.Sprintf("%s.%s", name, cleanExt)
}

// GenerateObjectName 生成对象键（源同名函数 UUID 策略）：dir + uuid.ext。
// 清理首尾斜杠，避免双斜杠或以斜杠开头的对象名。
func GenerateObjectName(fileDirectory, fileExt string) string {
	name := GeneraUUIDFileName(fileExt)
	dir := strings.Trim(fileDirectory, "/")
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// EnsureObjectName 确保扩展名存在后生成对象键（源同名函数）。
func EnsureObjectName(fileDirectory, sourceFileName, contentType string, fileContent []byte) string {
	fileExt := EnsureFileExtension(sourceFileName, contentType, fileContent)
	return GenerateObjectName(fileDirectory, fileExt)
}

// ExtractFileExtension 从文件名提取扩展名（不含点，小写；源同名函数）。
// 查找最后一个点作为扩展名分隔（点在开头不算）。
func ExtractFileExtension(fileName string) string {
	idx := strings.LastIndex(fileName, ".")
	if idx <= 0 {
		return ""
	}
	return strings.ToLower(fileName[idx+1:])
}

// EnsureFileExtension 确保文件后缀存在（源同名函数，返回值统一含前导点）：
// 按顺序从文件名、内容类型、文件内容检测；最终兜底 ".bin"。
func EnsureFileExtension(fileName, contentType string, fileContent []byte) string {
	if ext := ExtractFileExtension(fileName); ext != "" {
		return "." + ext
	}
	if ext := ContentTypeToFileExtension(contentType); ext != "" {
		return ext
	}
	if _, ext := DetectFileType(fileContent); ext != "" {
		return ext
	}
	return ".bin"
}
