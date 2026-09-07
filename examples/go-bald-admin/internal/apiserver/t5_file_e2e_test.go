package apiserver

// t5_file_e2e_test.go 文件管理 REST e2e（T5 验收）：真实 gin 引擎 + httptest +
// 真实 MinIO（§0 硬性：外部依赖禁止 fake/mock/stub）。MinIO 连接经 env 注入：
//
//	BALD_ADMIN_TEST_MINIO_ENDPOINT   host:port（必填，缺省则整组 Skip）
//	BALD_ADMIN_TEST_MINIO_ACCESS_KEY （必填）
//	BALD_ADMIN_TEST_MINIO_SECRET_KEY （必填）
//	BALD_ADMIN_TEST_MINIO_USE_SSL    "true"/"false"（默认 false）
//	BALD_ADMIN_TEST_MINIO_BUCKET     files 类兜底桶（默认 go-bald-admin-test）
//
// 验收点（移植计划 T5）：
//  1. 上传 → 下载内容一致（真实 MinIO 往返；PNG 魔术头嗅探 → images 桶）
//  2. 非白名单 MIME 拒绝（exe 魔术头）、目录穿越拒绝、超 50MiB 拒绝
//  3. 对象落桶可查（SDK StatObject）；删除后对象与元数据一并消失
//  4. P8 隔离：t-other 上传的文件 t-default 不可见
//  5. 授权：viewer 下载 200 / 上传 403（file 策略数据化）

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	miniov7 "github.com/minio/minio-go/v7"

	gingonic "github.com/gin-gonic/gin"

	filev1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/file/v1"
	authbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/auth"
	auditlogbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/auditlog"
	dictbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/dict"
	filebiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/file"
	menubiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/menu"
	permissionbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/permission"
	secretbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/secret"
	tenantbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/tenant"
	userbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/user"
	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
	miniooss "github.com/kalandramo/bald/oss/minio"
)

// png1x1 标准 1x1 透明 PNG（真实 PNG 字节流，嗅探必得 image/png）。
var png1x1 = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
	0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

// startFileREST 起真实 gin 引擎 + 真实 MinIO（env 注入；无 env 整组 Skip，
// 不引入任何 fake）。返回 base URL、file biz 与 MinIO Storage（落桶断言用）。
func startFileREST(t *testing.T) (string, *filebiz.Biz, *miniooss.Storage) {
	t.Helper()
	endpoint := os.Getenv("BALD_ADMIN_TEST_MINIO_ENDPOINT")
	accessKey := os.Getenv("BALD_ADMIN_TEST_MINIO_ACCESS_KEY")
	secretKey := os.Getenv("BALD_ADMIN_TEST_MINIO_SECRET_KEY")
	if endpoint == "" || accessKey == "" || secretKey == "" {
		t.Skip("BALD_ADMIN_TEST_MINIO_* not set: skip real-MinIO e2e (no fake allowed)")
	}
	bucket := os.Getenv("BALD_ADMIN_TEST_MINIO_BUCKET")
	if bucket == "" {
		bucket = "go-bald-admin-test"
	}

	if err := bootstrappkg.InitBridges(context.Background()); err != nil {
		t.Fatalf("InitBridges: %v", err)
	}
	mc := miniooss.NewStorage(&miniooss.Config{
		Endpoint:  endpoint,
		AccessKey: accessKey,
		SecretKey: secretKey,
		UseSsl:    os.Getenv("BALD_ADMIN_TEST_MINIO_USE_SSL") == "true",
	})
	// 连通性预检：MinIO 不可达即失败（快速失败优于半途 panic）
	if _, err := mc.SDK().ListBuckets(context.Background()); err != nil {
		t.Fatalf("minio unreachable: %v", err)
	}

	fb := filebiz.New(mc, bucket)
	e := gingonic.New()
	RegisterRoutes(e, authbiz.New(bootstrappkg.Signer), secretbiz.New(nil), tenantbiz.New(),
		userbiz.New(), menubiz.New(), permissionbiz.New(), dictbiz.New(nil), fb, auditlogbiz.New())
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return srv.URL, fb, mc
}

// multipartUpload multipart form 上传（file + directory），返回状态码与原始响应。
func multipartUpload(t *testing.T, base, tok, fileName, directory string, content []byte) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write file field: %v", err)
	}
	if directory != "" {
		if err := w.WriteField("directory", directory); err != nil {
			t.Fatalf("write directory field: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, base+"/v1/file/upload", &buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do upload: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp.StatusCode, raw
}

// statObject 断言辅助：对象是否存在（true=存在）。
func statObject(t *testing.T, mc *miniooss.Storage, bucket, key string) bool {
	t.Helper()
	_, err := mc.SDK().StatObject(context.Background(), bucket, key, miniov7.StatObjectOptions{})
	return err == nil
}

// TestFileREST_UploadDownloadLifecycle 上传 → 落桶可查 → 下载内容一致 →
// 删除 → 对象与元数据一并消失（T5 核心验收）。
func TestFileREST_UploadDownloadLifecycle(t *testing.T) {
	base, fb, mc := startFileREST(t)
	tok := tenantToken(t, "admin", "u-admin", "admin", "t-default")

	// 1. 上传 PNG（无目录）→ 201 + 嗅探元数据（images 桶 / image/png / sha256）。
	code, raw := multipartUpload(t, base, tok, "avatar.png", "", png1x1)
	if code != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", code, raw)
	}
	up := new(filev1.UploadFileResponse)
	decodePB(raw, up)
	f := up.GetFile()
	if f.GetId() == "" || f.GetBucketName() != "images" || f.GetMimeType() != "image/png" {
		t.Fatalf("unexpected file meta: %+v", f)
	}
	sum := sha256.Sum256(png1x1)
	if f.GetContentHash() != hex.EncodeToString(sum[:]) {
		t.Fatalf("content hash mismatch: %s", f.GetContentHash())
	}
	if int(f.GetSize()) != len(png1x1) {
		t.Fatalf("size=%d, want %d", f.GetSize(), len(png1x1))
	}
	if !strings.HasSuffix(f.GetSaveFileName(), ".png") || len(f.GetSaveFileName()) != 36 {
		// 保存名 = 32 位去横线 uuid + ".png"（源语义：Id 为带横线独立 uuid）。
		t.Fatalf("save_file_name=%s not <uuid32>.png", f.GetSaveFileName())
	}

	// 2. 对象落桶可查（真实 MinIO StatObject）。
	if !statObject(t, mc, f.GetBucketName(), f.GetFileDirectory()+"/"+f.GetSaveFileName()) {
		if !statObject(t, mc, f.GetBucketName(), f.GetSaveFileName()) {
			t.Fatalf("object %s/%s not found in bucket", f.GetBucketName(), f.GetSaveFileName())
		}
	}

	// 3. 下载（流式端点返回原始字节，非 JSON）：内容字节一致 + 响应头回传原始名。
	dlReq, _ := http.NewRequest(http.MethodGet, base+"/v1/file/"+f.GetId()+"/download", nil)
	dlReq.Header.Set("Authorization", "Bearer "+tok)
	dlResp, err := http.DefaultClient.Do(dlReq)
	if err != nil {
		t.Fatalf("do download: %v", err)
	}
	dlBody, _ := io.ReadAll(dlResp.Body)
	dlResp.Body.Close()
	if dlResp.StatusCode != http.StatusOK {
		t.Fatalf("download status=%d body=%s", dlResp.StatusCode, dlBody)
	}
	if !bytes.Equal(dlBody, png1x1) {
		t.Fatalf("downloaded content mismatch (%d bytes)", len(dlBody))
	}
	if ct := dlResp.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("download content-type=%s", ct)
	}
	if cd := dlResp.Header.Get("Content-Disposition"); !strings.Contains(cd, "avatar.png") {
		t.Fatalf("download content-disposition=%s", cd)
	}

	// 4. 删除：对象 + 元数据一并消失。
	if code, _ = callRaw(t, base, tok, http.MethodDelete, "/v1/file/"+f.GetId(), nil); code != http.StatusOK {
		t.Fatalf("delete status=%d", code)
	}
	if code, _ = callRaw(t, base, tok, http.MethodGet, "/v1/file/"+f.GetId(), nil); code != http.StatusNotFound {
		t.Fatalf("get after delete must 404, got %d", code)
	}
	if statObject(t, mc, f.GetBucketName(), f.GetSaveFileName()) {
		t.Fatalf("object still exists after delete")
	}
	_ = fb // biz 引用保持（供后续扩展断言）
}

// TestFileREST_MimeWhitelistAndLimits 非白名单 MIME 拒绝 / 目录穿越拒绝 /
// 超 50MiB 拒绝（T5 验收 2）。
func TestFileREST_MimeWhitelistAndLimits(t *testing.T) {
	base, _, _ := startFileREST(t)
	tok := tenantToken(t, "admin", "u-admin", "admin", "t-default")

	// 1. exe（MZ 魔术头 → application/x-msdownload）不在白名单 → 400。
	exe := append([]byte("MZ\x90\x00"), make([]byte, 128)...)
	if code, _ := multipartUpload(t, base, tok, "tool.exe", "", exe); code != http.StatusBadRequest {
		t.Fatalf("exe upload must 400, got %d", code)
	}

	// 2. 目录穿越（../evil）→ 400。
	if code, _ := multipartUpload(t, base, tok, "evil.png", "../evil", png1x1); code != http.StatusBadRequest {
		t.Fatalf("path traversal must 400, got %d", code)
	}

	// 3. 超 50MiB → 400（合法 MIME 大文件）。
	big := append([]byte("hello world\n"), make([]byte, 50<<20)...) // 50MiB+12
	if code, _ := multipartUpload(t, base, tok, "big.txt", "", big); code != http.StatusBadRequest {
		t.Fatalf("oversize upload must 400, got %d", code)
	}
}

// TestFileREST_TenantIsolation P8 隔离：t-default 上传的文件对 t-other 不可见
// （t-other 种子用户为 u-bob/viewer——tenantToken 直接签 claims，casbin subject
// 是 userID 且分组来自 seed users，故必须用真实种子用户）。
func TestFileREST_TenantIsolation(t *testing.T) {
	base, _, _ := startFileREST(t)
	defTok := tenantToken(t, "admin", "u-admin", "admin", "t-default")
	otherTok := tenantToken(t, "bob", "u-bob", "viewer", "t-other")

	code, raw := multipartUpload(t, base, defTok, "private.png", "", png1x1)
	if code != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", code, raw)
	}
	up := new(filev1.UploadFileResponse)
	decodePB(raw, up)

	// t-other 用户取 t-default 文件 → 404（租户过滤器自动生效）。
	if code, _ = callRaw(t, base, otherTok, http.MethodGet, "/v1/file/"+up.GetFile().GetId(), nil); code != http.StatusNotFound {
		t.Fatalf("cross-tenant get must 404, got %d", code)
	}
	// 跨租户列表同样隔离（file_name 前缀匹配不到他租户的记录）。
	code, raw = callRaw(t, base, otherTok, http.MethodGet, "/v1/file?file_name=private.png", nil)
	if code != http.StatusOK {
		t.Fatalf("list status=%d", code)
	}
	list := new(filev1.ListFilesResponse)
	decodePB(raw, list)
	if list.GetTotal() != 0 {
		t.Fatalf("cross-tenant list total=%d, want 0", list.GetTotal())
	}
	// 本租户 admin 可见。
	if code, _ = callRaw(t, base, defTok, http.MethodGet, "/v1/file/"+up.GetFile().GetId(), nil); code != http.StatusOK {
		t.Fatalf("same-tenant get must 200, got %d", code)
	}
}

// TestFileREST_ViewerPermission 授权：viewer 下载/读 200、上传/删除 403。
func TestFileREST_ViewerPermission(t *testing.T) {
	base, _, _ := startFileREST(t)
	adminTok := tenantToken(t, "admin", "u-admin", "admin", "t-default")
	viewerTok := tenantToken(t, "alice", "u-alice", "viewer", "t-default")

	code, raw := multipartUpload(t, base, adminTok, "logo.png", "", png1x1)
	if code != http.StatusCreated {
		t.Fatalf("admin upload status=%d", code)
	}
	up := new(filev1.UploadFileResponse)
	decodePB(raw, up)
	id := up.GetFile().GetId()

	if code, _ = callRaw(t, base, viewerTok, http.MethodGet, "/v1/file/"+id+"/download", nil); code != http.StatusOK {
		t.Fatalf("viewer download must 200, got %d", code)
	}
	if code, _ = multipartUpload(t, base, viewerTok, "no.png", "", png1x1); code != http.StatusForbidden {
		t.Fatalf("viewer upload must 403, got %d", code)
	}
	if code, _ = callRaw(t, base, viewerTok, http.MethodDelete, "/v1/file/"+id, nil); code != http.StatusForbidden {
		t.Fatalf("viewer delete must 403, got %d", code)
	}
}
