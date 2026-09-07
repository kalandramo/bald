package gin

// file.go 文件管理 REST handler（T5）。分组级 Authn（/v1 需登录）+ 路由级
// Authz（P9 归一化权限点 "file"）。上传走 multipart form（二进制流不走
// protojson），下载流式输出并携带原始文件名的 Content-Disposition；元数据
// 操作与 dict 同构（bindPB/writePB）。REST 路径用单数段 /v1/file，与 gRPC
// DefaultGRPCObject("FileService/...")="file" 同源——casbin 策略单写即覆盖
// 双协议。错误映射走 berrors → httperr（code 语义与 gRPC 侧一致）。

import (
	"errors"
	"io"
	"net/http"
	"net/url"

	gingonic "github.com/gin-gonic/gin"

	berrors "github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/berrors/httperr"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	mid "github.com/kalandramo/bald/pkg/middleware/gin"
	"google.golang.org/protobuf/types/known/timestamppb"

	filev1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/file/v1"
	filebiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/file"
	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
)

// RegisterFile 挂载文件管理路由（全部需认证 + file 资源权限，
// admin 写 / viewer 读）。
//   - POST   /v1/file/upload       multipart 上传（form: file + directory）
//   - GET    /v1/file              文件列表（?file_name=&mime_type=）
//   - GET    /v1/file/:id          取元数据
//   - GET    /v1/file/:id/download 下载内容（流式）
//   - DELETE /v1/file/:id          删除（对象 + 元数据）
func RegisterFile(
	e *gingonic.Engine,
	authenticator authn.Authenticator,
	authorizer authz.Authorizer,
	biz *filebiz.Biz,
) {
	authed := e.Group("/v1")
	authed.Use(mid.AuthnMiddleware(authenticator))
	authzMW := mid.AuthzMiddleware(authorizer,
		mid.WithObjectResolver(authz.DefaultHTTPObject),
		mid.WithActionResolver(authz.DefaultHTTPAction),
	)

	authed.POST("/file/upload", authzMW, func(c *gingonic.Context) {
		fh, err := c.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": "missing multipart form field \"file\""})
			return
		}
		f, err := fh.Open()
		if err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		defer f.Close()

		content, err := io.ReadAll(f)
		if err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}

		m, err := biz.Upload(c.Request.Context(), fh.Filename, c.PostForm("directory"), content)
		if err != nil {
			writeBizErr(c, err)
			return
		}
		writePB(c, http.StatusCreated, &filev1.UploadFileResponse{File: toFilePB(m)})
	})

	authed.GET("/file", authzMW, func(c *gingonic.Context) {
		fs, total, err := biz.List(c.Request.Context(), c.Query("file_name"), c.Query("mime_type"))
		if err != nil {
			writeBizErr(c, err)
			return
		}
		items := make([]*filev1.File, 0, len(fs))
		for _, f := range fs {
			items = append(items, toFilePB(f))
		}
		writePB(c, http.StatusOK, &filev1.ListFilesResponse{Items: items, Total: uint32(total)})
	})

	authed.GET("/file/:id", authzMW, func(c *gingonic.Context) {
		m, err := biz.Get(c.Request.Context(), c.Param("id"))
		if err != nil {
			writeBizErr(c, err)
			return
		}
		writePB(c, http.StatusOK, &filev1.GetFileResponse{File: toFilePB(m)})
	})

	authed.GET("/file/:id/download", authzMW, func(c *gingonic.Context) {
		m, data, err := biz.Download(c.Request.Context(), c.Param("id"))
		if err != nil {
			writeBizErr(c, err)
			return
		}
		mime := m.MimeType
		if mime == "" {
			mime = "application/octet-stream"
		}
		c.Header("Content-Disposition", `attachment; filename="`+url.PathEscape(m.FileName)+`"`)
		c.Data(http.StatusOK, mime, data)
	})

	authed.DELETE("/file/:id", authzMW, func(c *gingonic.Context) {
		deleted, err := biz.Delete(c.Request.Context(), c.Param("id"))
		if err != nil {
			writeBizErr(c, err)
			return
		}
		writePB(c, http.StatusOK, &filev1.DeleteFileResponse{Deleted: deleted})
	})
}

// writeBizErr 统一 biz 错误 → HTTP 状态码（berrors code 与 gRPC 侧同语义）。
func writeBizErr(c *gingonic.Context, err error) {
	code := http.StatusInternalServerError
	var be *berrors.Error
	if errors.As(err, &be) {
		code = httperr.CodeToHTTP(be.Code)
	}
	c.JSON(code, gingonic.H{"error": err.Error()})
}

// toFilePB 模型 → proto（gin 侧；Size 用 uint32 直传避免 JSON 字符串化）。
func toFilePB(m *authmodel.File) *filev1.File {
	return &filev1.File{
		Id:            m.ID,
		Provider:      m.Provider,
		BucketName:    m.BucketName,
		SaveFileName:  m.SaveFileName,
		FileDirectory: m.FileDirectory,
		FileName:      m.FileName,
		Extension:     m.Extension,
		ContentHash:   m.ContentHash,
		Size:          uint32(m.Size),
		LinkUrl:       m.LinkUrl,
		MimeType:      m.MimeType,
		CreatedBy:     m.CreatedBy,
		CreatedAt:     timestamppb.New(m.CreatedAt),
		UpdatedAt:     timestamppb.New(m.UpdatedAt),
	}
}
