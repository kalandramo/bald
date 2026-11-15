package codegen

// buf_layout_test.go 钉住 go_package 前缀探测的两个现实写法：
//
//	模板布局（gen app 播种）与早期布局都只有一条无限定 override；
//	buf 生态还常见「第一条带 path 限定、第二条才是真前缀」的写法——
//	曾因正则抓第一个条目把新 proto 的 go_package 写进第三方包路径
//	（adv-codegen Bug3，b4 repro 实测绑定 import 不存在的包）。
import (
	"testing"
)

func TestBufLayout_FirstUnqualifiedGoPackagePrefix(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "single unqualified entry",
			src: `version: v2
managed:
  enabled: true
  override:
    - file_option: go_package_prefix
      value: m1.test/app/api/gen/go
plugins:
  - local: protoc-gen-go
`,
			want: "m1.test/app/api/gen/go",
		},
		{
			name: "path-qualified first, real prefix second",
			src: `version: v2
managed:
  enabled: true
  override:
    - file_option: go_package_prefix
      value: google.golang.org/protobuf/types/gofeaturepb
      path: google/protobuf
    - file_option: go_package_prefix
      value: m1.test/app/api/gen/go
plugins:
  - remote: buf.build/protocolbuffers/go
`,
			want: "m1.test/app/api/gen/go",
		},
		{
			name: "only path-qualified entries",
			src: `version: v2
managed:
  enabled: true
  override:
    - file_option: go_package_prefix
      value: google.golang.org/protobuf/types/gofeaturepb
      path: google/protobuf
`,
			want: "",
		},
		{
			name: "no managed section",
			src: `version: v2
plugins:
  - local: protoc-gen-go
`,
			want: "",
		},
		{
			name: "quoted value",
			src: `version: v2
managed:
  enabled: true
  override:
    - file_option: go_package_prefix
      value: "m1.test/app/api/gen"
`,
			want: "m1.test/app/api/gen",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstUnqualifiedGoPackagePrefix(tc.src); got != tc.want {
				t.Errorf("firstUnqualifiedGoPackagePrefix = %q, want %q", got, tc.want)
			}
		})
	}
}
