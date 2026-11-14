package remotev1

import "context"

// Transport 是业务层依赖的最小收发接口；MQTT 和测试假实现都满足它。
type Transport interface {
	Subscribe(ctx context.Context, filters []string, handler func(topic string, payload []byte)) error
	Publish(ctx context.Context, topic string, payload []byte, qos byte, retain bool) error
}
