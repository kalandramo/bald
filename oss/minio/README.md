# minio 包说明

## 概述

`minio` 包基于 [minio-go](https://github.com/minio/minio-go) SDK 封装，提供 MinIO 客户端创建与轻量上传/下载能力。

## 安装

```bash
go get github.com/kalandramo/bald/oss/minio
```

## Config

```go
type Config struct {
    Endpoint  string // MinIO 服务地址，例如 "127.0.0.1:9000"
    AccessKey string // 访问密钥
    SecretKey string // 秘密密钥
    Token     string // 临时凭证 token（可为空）
    UseSsl    bool   // 是否启用 HTTPS
}
```

## API

| 函数/方法 | 说明 |
|-----------|------|
| `NewClient(cfg *Config) *minio.Client` | 创建 MinIO SDK 客户端 |
| `NewStorage(cfg *Config) *Storage` | 创建带封装的 Storage |
| `(*Storage).SDK() *minio.Client` | 获取底层 SDK 客户端 |
| `(*Storage).PutObject(ctx, bucket, key, body, contentType) (minio.UploadInfo, error)` | 上传对象（每次指定 bucket） |
| `(*Storage).GetObject(ctx, bucket, key) (*minio.Object, error)` | 下载对象（每次指定 bucket） |

## 使用示例

### 创建客户端

```go
import "github.com/kalandramo/bald/oss/minio"

cfg := &minio.Config{
    Endpoint:  "127.0.0.1:9000",
    AccessKey: "minioadmin",
    SecretKey: "minioadmin",
    UseSsl:    false,
}

client := minio.NewClient(cfg)
_ = client
```

### 上传与下载

```go
import (
    "context"
    "strings"

    "github.com/kalandramo/bald/oss/minio"
)

func main() {
    ctx := context.Background()

    storage := minio.NewStorage(&minio.Config{
        Endpoint:  "127.0.0.1:9000",
        AccessKey: "minioadmin",
        SecretKey: "minioadmin",
        UseSsl:    false,
    })

    // 上传
    _, err := storage.PutObject(ctx, "my-bucket", "demo/hello.txt",
        strings.NewReader("hello world"), "text/plain")
    if err != nil {
        panic(err)
    }

    // 下载
    obj, err := storage.GetObject(ctx, "my-bucket", "demo/hello.txt")
    if err != nil {
        panic(err)
    }
    defer obj.Close()
}
```

## 与 `oss/s3` 的区别

| 对比项 | `oss/minio` | `oss/s3` |
|--------|-------------|----------|
| 底层 SDK | minio/minio-go | aws-sdk-go-v2 |
| `PutObject` / `GetObject` | 需显式传 `bucket` 参数 | 使用 Config 中的默认 `bucket` |
| 适用场景 | 以 MinIO 为核心 | AWS S3 或多云 S3 兼容服务 |

## 错误

| 变量 | 说明 |
|------|------|
| `ErrNilClient` | 客户端未初始化 |
| `ErrEmptyBucket` | bucket 为空 |
| `ErrEmptyObjectKey` | 对象 key 为空 |
| `ErrNilObjectBody` | 上传内容为 nil |
