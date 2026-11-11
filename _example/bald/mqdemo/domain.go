// Package mqdemo 是 mq 业务域：演示 bootstrap.broker.redis 段的消费者面。
//
// 装配链路：AppSpec.bootstrap.broker.redis 段 → broker/redis/contract 的
// AppProvider（custom.go 接线）在阶段 B 构造并连接 Redis Broker，产物落
// 资源容器（键 broker.redis.redis）；本域在 Wire 阶段取用并订阅演示主题。
//
// 本域不进 AppSpec.domains——域声明三选一（service|services|http_only）
// 都要求 proto 契约，纯消费域无从派生；它由 custom.go 按角色手工接线
// （worker 是消费角色），这正是 customOptions 的职责面。
package mqdemo

import (
	"context"
	"fmt"

	brokerrediscontract "github.com/kalandramo/bald/broker/redis/contract"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/appkit"
)

// RedisBroker 是 broker.redis 段产物的资源键（ADR-16：标准键由
// broker/redis/contract.RedisKey 导出，消费者不再手写魔法串）。
var RedisBroker = brokerrediscontract.RedisKey

// Topic 是演示主题（pub/sub：发布即广播，worker 角色的订阅者收到后记日志）。
const Topic = "bald.demo.events"

// DemoEvent 是演示事件载荷（JSON 绑定，字段即消息契约）。
type DemoEvent struct {
	Seq  int    `json:"seq"`
	From string `json:"from"`
}

// Domain 是 mqdemo 域的装配声明值。
var Domain = appkit.Domain{
	Name:      "mqdemo",
	Declare:   declare,
	ApplyNode: applyNode,
	Reconcile: reconcileNode,
}

// declare 声明域拓扑：订阅节点挂在 broker.redis 段上——段缺席（未来裁剪）
// 计划期即无此节点，消费面随声明走，不靠运行期猜测。
func declare(_ context.Context, _ appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{{
		Key:      "domain.mqdemo.sub",
		Segments: []string{"broker.redis"},
		Phase:    appkit.PhaseWire,
		Priority: 10,
	}}, nil
}

func applyNode(ctx context.Context, app *appkit.App, node appkit.Node) error {
	switch node.Key {
	case "domain.mqdemo.sub":
		return wire(ctx, app)
	}
	return fmt.Errorf("mqdemo: unknown declared node %q", node.Key)
}

func reconcileNode(ctx context.Context, app *appkit.App, node appkit.Node) error {
	switch node.Key {
	case "domain.mqdemo.sub":
		return wire(ctx, app)
	}
	return fmt.Errorf("mqdemo: unknown reconcile node %q", node.Key)
}

// wire 取用阶段 B 产物并订阅。资源缺席时域保持惰性——订阅面是可选能力，
// 段驱动 Provider 只在段存在时装配，这里同样不硬性要求。
func wire(ctx context.Context, app *appkit.App) error {
	b, ok := RedisBroker.Resource(app)
	if !ok {
		log.Info(ctx, "mqdemo: broker resource absent, subscription skipped")
		return nil
	}
	_, err := broker.Subscribe(b, Topic,
		func(ctx context.Context, topic string, _ broker.Headers, ev *DemoEvent) error {
			log.Info(ctx, "mqdemo event received", "topic", topic, "seq", ev.Seq, "from", ev.From)
			return nil
		})
	if err != nil {
		return fmt.Errorf("mqdemo: subscribe %q: %w", Topic, err)
	}
	return nil
}
