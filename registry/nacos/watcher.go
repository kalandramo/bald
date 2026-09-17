package nacos

import (
	"context"
	"fmt"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/nacos-group/nacos-sdk-go/v2/model"
	v2vo "github.com/nacos-group/nacos-sdk-go/v2/vo"

	registry "github.com/kalandramo/bald/registry"
)

var _ registry.Watcher = (*watcher)(nil)

// watcher 订阅 nacos 服务变更：Subscribe 回调推信号，Next 拉取最新实例。
type watcher struct {
	serviceName    string
	clusters       []string
	groupName      string
	ctx            context.Context
	cancel         context.CancelFunc
	watchChan      chan struct{}
	cli            naming_client.INamingClient
	kind           string
	subscribeParam *v2vo.SubscribeParam
}

func newWatcher(ctx context.Context, cli naming_client.INamingClient, serviceName, groupName, kind string, clusters []string) (*watcher, error) {
	w := &watcher{
		serviceName: serviceName,
		clusters:    clusters,
		groupName:   groupName,
		cli:         cli,
		kind:        kind,
		watchChan:   make(chan struct{}, 1),
	}
	w.ctx, w.cancel = context.WithCancel(ctx)

	w.subscribeParam = &v2vo.SubscribeParam{
		ServiceName: serviceName,
		Clusters:    clusters,
		GroupName:   groupName,
		SubscribeCallback: func([]model.Instance, error) {
			select {
			case w.watchChan <- struct{}{}:
			default:
			}
		},
	}
	e := w.cli.Subscribe(w.subscribeParam)
	select {
	case w.watchChan <- struct{}{}:
	default:
	}
	return w, e
}

// Next 阻塞至变更信号或 ctx 结束，返回最新实例列表。
func (w *watcher) Next(ctx context.Context) ([]*registry.ServiceInstance, error) {
	select {
	case <-w.ctx.Done():
		return nil, w.ctx.Err()
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-w.watchChan:
	}
	res, err := w.cli.GetService(v2vo.GetServiceParam{
		ServiceName: w.serviceName,
		GroupName:   w.groupName,
		Clusters:    w.clusters,
	})
	if err != nil {
		return nil, err
	}
	items := make([]*registry.ServiceInstance, 0, len(res.Hosts))
	for _, in := range res.Hosts {
		kind := w.kind
		if k, ok := in.Metadata["kind"]; ok {
			kind = k
		}
		items = append(items, &registry.ServiceInstance{
			ID:        in.InstanceId,
			Name:      res.Name,
			Version:   in.Metadata["version"],
			Metadata:  in.Metadata,
			Endpoints: []string{fmt.Sprintf("%s://%s:%d", kind, in.Ip, in.Port)},
		})
	}
	return items, nil
}

// Stop 取消订阅并释放 watcher。
func (w *watcher) Stop() error {
	err := w.cli.Unsubscribe(w.subscribeParam)
	w.cancel()
	return err
}
