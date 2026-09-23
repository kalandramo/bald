package baldmongo

import (
	"context"
	"os"
	"testing"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// testURI 返回测试用 MongoDB URI（默认本地 27017，可用 BALD_MONGO_URI 覆盖）。
func testURI() string {
	if u := os.Getenv("BALD_MONGO_URI"); u != "" {
		return u
	}
	return "mongodb://127.0.0.1:27017"
}

// newTestDB 连接测试 MongoDB 并返回一个每次调用唯一命名的 database，
// 测试结束自动 Drop + Disconnect。每个测试独立 database，无跨测试串扰。
//
// 集成测试：-short 下跳过（CI 无 mongod 服务，见 .github/workflows/ci.yml）；
// 非 -short 且 mongod 不可达时同样 Skip（环境缺失，非实现缺陷）。
func newTestDB(t *testing.T) *mongo.Database {
	t.Helper()
	if testing.Short() {
		t.Skip("集成测试：依赖真实 MongoDB（:27017），-short 下跳过")
	}
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(testURI()))
	if err != nil {
		t.Fatalf("baldmongo: connect: %v", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(ctx)
		t.Skipf("baldmongo: ping %s 失败（需真实 mongod，见 README）: %v", testURI(), err)
	}
	dbName := "baldmongo_test_" + t.Name()
	// 清理同名遗留 database（上次异常中断可能残留）。
	_ = client.Database(dbName).Drop(ctx)
	t.Cleanup(func() {
		_ = client.Database(dbName).Drop(context.Background())
		_ = client.Disconnect(context.Background())
	})
	return client.Database(dbName)
}
