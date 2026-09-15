package contract

import (
	"context"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	mongocrud "github.com/kalandramo/bald-crud/mongodb"
)

func TestType(t *testing.T) {
	if Type != "mongodb" {
		t.Errorf("Type = %q, want %q", Type, "mongodb")
	}
}

// 段缺失 / 必填字段缺失 fail-fast（不触真连）。

func TestProvider_MissingSection(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Database{})
	if err == nil {
		t.Fatal("Provider with missing mongodb section should fail")
	}
	want := `database: type="mongodb" but mongodb section is missing`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestProvider_MissingURI(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Database{
		Mongodb: &bootstrapv1.Database_MongoDB{Database: "db"},
	})
	if err == nil {
		t.Fatal("Provider with empty mongodb.uri should fail")
	}
	want := "database: mongodb.uri is required"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// buildOptions 纯函数映射断言（不触真连）。

func TestBuildOptions_FullFields(t *testing.T) {
	opts := buildOptions(&bootstrapv1.Database_MongoDB{
		Uri:                           "mongodb://127.0.0.1:27017",
		Database:                      "app",
		Username:                      "u",
		Password:                      "p",
		TimeoutSeconds:                5,
		ConnectTimeoutSeconds:         6,
		ServerSelectionTimeoutSeconds: 7,
		HeartbeatIntervalSeconds:      8,
		MaxConnIdleTimeSeconds:        9,
	})
	// URI + Database + Credentials + 5 个超时 = 8
	if len(opts) != 8 {
		t.Fatalf("len(opts) = %d, want 8", len(opts))
	}
}

func TestBuildOptions_MinimalFields(t *testing.T) {
	opts := buildOptions(&bootstrapv1.Database_MongoDB{
		Uri: "mongodb://127.0.0.1:27017",
	})
	// 仅 URI（无 database/credentials/超时）
	if len(opts) != 1 {
		t.Fatalf("len(opts) = %d, want 1", len(opts))
	}
}

// 类型守卫：Provider 签名与 bootstrap.DatabaseProvider 结构化兼容。
var _ func(context.Context, *bootstrapv1.Database) (any, func(), error) = Provider

// With* 构造期无副作用（引用防未使用告警的语义锚点）。
var _ = mongocrud.WithURI
