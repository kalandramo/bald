package etcd

import (
	"encoding/json"

	registry "github.com/kalandramo/bald/pkg/registry"
)

// marshal 序列化实例为 etcd value（JSON）。
func marshal(si *registry.ServiceInstance) (string, error) {
	data, err := json.Marshal(si)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// unmarshal 从 etcd value 反序列化实例。
func unmarshal(data []byte) (si *registry.ServiceInstance, err error) {
	err = json.Unmarshal(data, &si)
	return
}
