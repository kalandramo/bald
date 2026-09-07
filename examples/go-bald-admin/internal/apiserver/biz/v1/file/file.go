package file

// file.go 文件模块业务层（T5）。语义对齐源 go-wind-admin file_transfer_service.go：
// 上传校验（大小上限/MIME 白名单/目录安全）→ 按内容类型分桶 + 兜底建桶 →
// MinIO PutObject → 元数据落库（SHA256/大小/扩展名/原始名）；下载按元数据重建
// 对象键读取内容；删除先删对象再删元数据。
//
// 文件是租户级业务数据（源 mixin TenantID）：store 层 P8 自动注入与隔离
// TenantID（Where.T）；CreatedBy 取认证后的 UserID（contextx）。
// store 经请求期包级引用（bootstrap.FileStore，InitBridges 装配后可用）。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/google/uuid"
	miniov7 "github.com/minio/minio-go/v7"

	berrors "github.com/kalandramo/bald/berrors"
	miniooss "github.com/kalandramo/bald/oss/minio"
	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/store"

	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
)

// Biz 文件业务：MinIO 客户端 + 兜底桶名。FileStore 经 bootstrap 包级引用。
type Biz struct {
	mc            *miniooss.Storage
	defaultBucket string // file.bucket 配置：files 类内容的兜底桶名
}

// New 构造 File biz（mc 由 bootstrap 桥接装配；defaultBucket 为 file.bucket 配置，
// 空 = 沿用源默认 "files"）。
//
// 注意：InitializeBiz 在 main 早期执行，此时值拷贝 bootstrap.MinioStorage /
// FileBucket 必为 nil（InitBridges 在 appkit.BeforeStart 才赋值）——e2e 测试因
// 装配时序不同无法暴露该盲区，T8 §9 全序列真调时发现主链路 storage_unavailable。
// 因此构造后必须在 BeforeStart（InitBridges 之后）调用 SetStorage 补注
// （对齐 appkit.SetRegistrar 的"构造期 nil + 运行期接线"模式）。
func New(mc *miniooss.Storage, defaultBucket string) *Biz {
	return &Biz{mc: mc, defaultBucket: defaultBucket}
}

// SetStorage 运行期注入存储依赖（main.go BeforeStart 在 InitBridges 之后调用）。
func (b *Biz) SetStorage(mc *miniooss.Storage, defaultBucket string) {
	b.mc = mc
	b.defaultBucket = defaultBucket
}

// fileStore 请求期读取（InitBridges 装配后可用），避免构造期对全局的强依赖。
func (b *Biz) fileStore() *store.Store[authmodel.File] {
	return bootstrappkg.FileStore
}

// Upload 上传文件（语义对齐源 UploadFile）：校验 → 分桶 → 建桶 → PutObject →
// 元数据落库。返回登记后的元数据（ID 为新 uuid，FileName 保留原始名）。
func (b *Biz) Upload(ctx context.Context, fileName, fileDirectory string, content []byte) (*authmodel.File, error) {
	if b.mc == nil {
		return nil, berrors.Internal("file/storage_unavailable")
	}
	if len(content) == 0 {
		return nil, berrors.BadRequest("file/upload_empty")
	}
	if int64(len(content)) > MaxUploadSize {
		return nil, berrors.BadRequest("file/upload_too_large")
	}
	// 以嗅探结果为准（扩展名可伪装），白名单外一律拒绝
	mimeType, _ := DetectFileType(content)
	if !IsAllowedMimeType(mimeType) {
		return nil, berrors.BadRequest("file/mime_not_allowed")
	}
	if !IsFileDirectorySafe(fileDirectory) {
		return nil, berrors.BadRequest("file/directory_unsafe")
	}

	ext := EnsureFileExtension(fileName, mimeType, content)
	objectName := GenerateObjectName(fileDirectory, ext)

	bucket := b.bucketFor(mimeType)
	if err := b.ensureBucket(ctx, bucket); err != nil {
		return nil, err
	}

	info, err := b.mc.PutObject(ctx, bucket, objectName, bytes.NewReader(content), mimeType)
	if err != nil {
		return nil, fmt.Errorf("file: put object: %w", err)
	}

	dir, base := parseKey(info.Key)
	m := &authmodel.File{
		ID:            uuid.NewString(), // 源 recordFile：Id 独立 uuid
		Provider:      "minio",
		BucketName:    bucket,
		SaveFileName:  base + ext, // 保存名 = uuid + 扩展名（对象键重建用）
		FileDirectory: dir,
		FileName:      fileName, // 原始文件名（Content-Disposition 友好）
		Extension:     ext,
		ContentHash:   sha256Hex(content),
		Size:          int64(info.Size),
		MimeType:      mimeType,
		CreatedBy:     contextx.UserIDFromContext(ctx),
	}
	if err := b.fileStore().Create(ctx, m); err != nil {
		return nil, fmt.Errorf("file: create record: %w", err)
	}
	return m, nil
}

// Download 下载文件内容（语义对齐源 DownloadFile）：按元数据重建对象键读取。
func (b *Biz) Download(ctx context.Context, id string) (*authmodel.File, []byte, error) {
	if b.mc == nil {
		return nil, nil, berrors.Internal("file/storage_unavailable")
	}
	m, err := b.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	obj, err := b.mc.GetObject(ctx, m.BucketName, b.objectKey(m))
	if err != nil {
		return nil, nil, fmt.Errorf("file: get object: %w", err)
	}
	defer obj.Close()

	data, err := io.ReadAll(obj)
	if err != nil {
		return nil, nil, fmt.Errorf("file: read object: %w", err)
	}
	return m, data, nil
}

// Get 取文件元数据（租户隔离自动）。
func (b *Biz) Get(ctx context.Context, id string) (*authmodel.File, error) {
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("id", id))
	m, err := b.fileStore().Get(ctx, w.T(ctx))
	if err != nil || m == nil {
		return nil, berrors.NotFound("file/not_found")
	}
	return m, nil
}

// List 列出文件（租户隔离自动；原始文件名 LIKE / 内容类型精确过滤）。
func (b *Biz) List(ctx context.Context, fileName, mimeType string) ([]*authmodel.File, int64, error) {
	w := &store.Where{}
	if fileName != "" {
		w.Filters = append(w.Filters, store.Like("file_name", fileName+"%"))
	}
	if mimeType != "" {
		w.Filters = append(w.Filters, store.Eq("mime_type", mimeType))
	}
	return b.fileStore().List(ctx, w.T(ctx))
}

// Delete 删除文件（语义对齐源 Delete）：先删对象，再删元数据。
func (b *Biz) Delete(ctx context.Context, id string) (string, error) {
	if b.mc == nil {
		return "", berrors.Internal("file/storage_unavailable")
	}
	m, err := b.Get(ctx, id)
	if err != nil {
		return "", err
	}
	if err := b.mc.SDK().RemoveObject(ctx, m.BucketName, b.objectKey(m), miniov7.RemoveObjectOptions{}); err != nil {
		return "", fmt.Errorf("file: remove object: %w", err)
	}
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("id", id))
	if err := b.fileStore().Delete(ctx, w.T(ctx)); err != nil {
		return "", fmt.Errorf("file: delete record: %w", err)
	}
	return id, nil
}

// bucketFor 按内容类型分桶；files 兜底桶名由 file.bucket 配置覆盖（空 = "files"）。
func (b *Biz) bucketFor(contentType string) string {
	bucket := ContentTypeToBucketName(contentType)
	if bucket == BucketFiles && b.defaultBucket != "" {
		return b.defaultBucket
	}
	return bucket
}

// ensureBucket 桶兜底创建（源 EnsureBucket 语义）：不存在则创建（默认区域）。
func (b *Biz) ensureBucket(ctx context.Context, bucket string) error {
	exists, err := b.mc.SDK().BucketExists(ctx, bucket)
	if err != nil {
		return fmt.Errorf("file: bucket exists: %w", err)
	}
	if exists {
		return nil
	}
	if err := b.mc.SDK().MakeBucket(ctx, bucket, miniov7.MakeBucketOptions{}); err != nil {
		return fmt.Errorf("file: make bucket: %w", err)
	}
	return nil
}

// objectKey 由元数据重建对象键：dir/save_name（空目录直接 save_name）。
func (b *Biz) objectKey(m *authmodel.File) string {
	if m.FileDirectory == "" {
		return m.SaveFileName
	}
	return m.FileDirectory + "/" + m.SaveFileName
}

// parseKey 拆对象键为目录与去扩展名基础名（源 parseKey 语义）。
func parseKey(key string) (dir, base string) {
	parts := strings.SplitN(key, "/", 2)
	if len(parts) == 2 {
		dir = parts[0]
		base = parts[1]
	} else {
		base = parts[0]
	}
	base = strings.TrimSuffix(base, path.Ext(base))
	return dir, base
}

// sha256Hex 内容 SHA256 hex（源 ContentHash 语义）。
func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
