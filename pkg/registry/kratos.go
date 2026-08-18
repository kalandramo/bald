package registry

import (
	"context"

	kratosregistry "github.com/go-kratos/kratos/v3/registry"
)

// FromKratos 将 kratos 的 registry.Registrar 桥接为 bald 的 Registrar。
// 这样 kratos 生态的 etcd/consul/nacos 后端可即插即用。
func FromKratos(r kratosregistry.Registrar) Registrar {
	return &kratosAdapter{inner: r}
}

type kratosAdapter struct {
	inner kratosregistry.Registrar
}

func (a *kratosAdapter) Register(ctx context.Context, instance *ServiceInstance) error {
	return a.inner.Register(ctx, toKratos(instance))
}

func (a *kratosAdapter) Deregister(ctx context.Context, instance *ServiceInstance) error {
	return a.inner.Deregister(ctx, toKratos(instance))
}

func toKratos(in *ServiceInstance) *kratosregistry.ServiceInstance {
	return &kratosregistry.ServiceInstance{
		ID:        in.ID,
		Name:      in.Name,
		Version:   in.Version,
		Metadata:  in.Metadata,
		Endpoints: in.Endpoints,
	}
}
