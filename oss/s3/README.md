# s3 包说明

## 概述

`s3` 包基于 [aws-sdk-go-v2](https://github.com/aws/aws-sdk-go-v2) 封装，提供 S3 客户端创建与轻量上传/下载能力。

除 AWS S3 外，也兼容所有实现 S3 API 的对象存储（MinIO、Ceph RGW、LocalStack、RustFS、Garage、SeaweedFS、Wasabi、Backblaze B2、Cloudflare R2、腾讯云 COS 等），通过配置自定义 `endpoint` 即可接入。

## 安装

```bash
go get github.com/kalandramo/bald/oss/s3
```

## Config

```go
type Config struct {
    Endpoint       string // S3 服务地址（留空则由 AWS SDK 按 region 自动解析）
    Region         string // 区域（留空默认 "us-east-1"）
    AccessKey      string // 访问密钥
    SecretKey      string // 秘密密钥
    Token          string // 临时凭证 token（可为空）
    UseSsl         bool   // 是否启用 HTTPS（Endpoint 无协议头时据此补齐）
    ForcePathStyle bool   // 是否使用路径风格 URL（自建/本地服务建议 true）
    Bucket         string // Storage 默认使用的 bucket
}
```

## API

| 函数/方法 | 说明 |
|-----------|------|
| `NewClient(cfg *Config) *s3.Client` | 创建 AWS S3 SDK 客户端 |
| `NewStorage(cfg *Config) *Storage` | 创建带默认 bucket 的 Storage |
| `(*Storage).SDK() *s3.Client` | 获取底层 SDK 客户端 |
| `(*Storage).Bucket() string` | 获取当前默认 bucket |
| `(*Storage).PutObject(ctx, key, body, contentType) (*s3.PutObjectOutput, error)` | 上传对象（使用配置中的默认 bucket） |
| `(*Storage).GetObject(ctx, key) (*s3.GetObjectOutput, error)` | 下载对象（使用配置中的默认 bucket） |

## 使用示例

### AWS S3

```go
import kss3 "github.com/kalandramo/bald/oss/s3"

cfg := &kss3.Config{
    Endpoint:  "s3.ap-southeast-1.amazonaws.com",
    Region:    "ap-southeast-1",
    Bucket:    "my-bucket",
    AccessKey: "your-access-key",
    SecretKey: "your-secret-key",
    UseSsl:    true,
}

storage := kss3.NewStorage(cfg)
```

### MinIO / 本地兼容服务

```go
cfg := &kss3.Config{
    Endpoint:       "127.0.0.1:9000",
    Region:         "us-east-1",
    Bucket:         "my-bucket",
    AccessKey:      "minioadmin",
    SecretKey:      "minioadmin",
    UseSsl:         false,
    ForcePathStyle: true, // 自建/本地服务建议开启
}

storage := kss3.NewStorage(cfg)
```

### 上传与下载

```go
ctx := context.Background()

// 上传
_, err := storage.PutObject(ctx, "demo/hello.txt",
    strings.NewReader("hello world"), "text/plain")
if err != nil {
    panic(err)
}

// 下载
resp, err := storage.GetObject(ctx, "demo/hello.txt")
if err != nil {
    panic(err)
}
defer resp.Body.Close()
```

## `ForcePathStyle` 说明

| 值 | URL 形式 | 适用场景 |
|----|---------|---------|
| `false`（默认） | `https://my-bucket.s3.amazonaws.com/key` | AWS S3、Wasabi、Cloudflare R2、腾讯云 COS 等云厂商 |
| `true` | `https://s3.example.com/my-bucket/key` | MinIO、LocalStack、RustFS、Garage、SeaweedFS、Ceph RGW 等自建/本地服务 |

不确定时，参考经验：**云厂商官方服务用 `false`，自建/本地服务用 `true`**。

## 与 `oss/minio` 的区别

| 对比项 | `oss/s3` | `oss/minio` |
|--------|----------|-------------|
| 底层 SDK | aws-sdk-go-v2 | minio/minio-go |
| `PutObject` / `GetObject` | 使用 Config 中的默认 `bucket` | 需显式传 `bucket` 参数 |
| 适用场景 | AWS S3 或多云 S3 兼容服务 | 以 MinIO 为核心 |

## 错误

| 变量 | 说明 |
|------|------|
| `ErrNilClient` | 客户端未初始化 |
| `ErrEmptyBucket` | bucket 为空 |
| `ErrEmptyObjectKey` | 对象 key 为空 |
| `ErrNilObjectBody` | 上传内容为 nil |
