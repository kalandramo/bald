// provider.go —— 段驱动装配的加法式 Provider 导出（生态接入，
// 范本 contrib/store-gorm 的 AppProvider）。既有构造逻辑（Provider 契约
// 工厂，段字段 → cron.Option 映射 + 任务挂载）保留不动；本文件把段驱动
// 接入面收敛到 pkg/appkit/segment：AppProvider() 消费契约 server.cron 段，
// 节点形状由段族表派生，人只写 Build 主体，产物为 transport.Server 进
// 服务器列表。
//
// server 段 co-provider：与 std 的 http/grpc 并存（不同段路径并存是引擎
// 的既有语义）；server 段在业务 BeforeStart 钩子之后构建
// （phaseLate，服务器构造消费业务建好的运行期资源）。
//
// 本包的 Option（WithJobs / WithServerOptions）经 AppProvider 的变参透传，
// 业务接线入口不变。
//
// 放置说明（与 store-gorm 的结构差异）：本模块的契约映射逻辑在 contract
// 包（cron 包保持零契约依赖），root 包反向 import 本包——段驱动导出只能
// 落在这里，复用既有 Provider 而非复制映射。
package contract

import (
	"context"
	"fmt"

	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/pkg/appkit/segment"
)

// AppProvider 返回消费契约 server.cron 段的 appkit.Provider（段路径
// "server.cron"；段缺失即被引擎跳过，合法）。
//
// 命名说明（store-gorm 同例）：本包既有 Provider（契约驱动的工厂函数）
// 占用 Provider 标识符且保留不动，故段驱动装配的导出名取 AppProvider。
//
// Build 语义：server.cron 段在 → 经既有工厂构造（seconds/gracefullyShutdown
// 「未设置即用实现默认」，location 解析失败 fail-fast）→ 产物
// transport.Server 落服务器列表 → cleanup 透传（cron 无外部资源，nil 合法）。
func AppProvider(opts ...Option) appkit.Provider {
	factory := Provider(opts...)
	// 节点形状由段族表派生（server 族：PhaseServe，其余零值）——与迁移前
	// 本包手写四件套逐字段一致；人只保留 Build 主体。
	return segment.New("server.cron", func(ctx context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
		serverSec := s.Config.GetServer()
		if serverSec == nil {
			return nil, nil, fmt.Errorf("transport/cron: server section is missing " +
				"(provider matched by segment path; report this as a bug)")
		}
		srv, cleanup, err := factory(ctx, serverSec)
		if err != nil {
			return nil, nil, err
		}
		return srv, appkit.CleanupFrom(cleanup), nil
	})
}
