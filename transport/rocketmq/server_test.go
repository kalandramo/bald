package rocketmq

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/kalandramo/bald/log"
	api "github.com/kalandramo/bald/transport/rocketmq/testapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/broker/rocketmq/option"
	"github.com/kalandramo/bald/broker/rocketmq/v2"
)

const (
	// Name Server address
	testBroker = "127.0.0.1:9876"

	testTopic     = "test_topic"
	testGroupName = "CID_ONSAPI_OWNER"
)

func handleHygrothermograph(_ context.Context, topic string, headers broker.Headers, msg *api.Hygrothermograph) error {
	log.Info(nil, fmt.Sprintf("Topic %s, Headers: %+v, Payload: %+v\n", topic, headers, msg))
	return nil
}

func TestServer(t *testing.T) {
	if testing.Short() {
		t.Skip("manual test: blocks on signal, requires live broker")
	}
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	ctx := context.Background()

	srv := NewServer(
		option.DriverTypeV2,
		WithNameServer([]string{testBroker}),
		//WithNameServerDomain("http://nsaddr.rmq.cloud.tencent.com"),
		WithCodec("json"),
	)

	err := RegisterSubscriber(srv, ctx, testTopic, testGroupName, handleHygrothermograph)
	assert.Nil(t, err)

	require.NoError(t, srv.Start(ctx))

	defer func() {
		if err := srv.Stop(ctx); err != nil {
			t.Errorf("expected nil got %v", err)
		}
	}()

	<-interrupt
}

func TestClient(t *testing.T) {
	if testing.Short() {
		t.Skip("manual test: blocks on signal, requires live broker")
	}
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	ctx := context.Background()

	b := rocketmq.NewBroker(broker.WithCodec("json"),
		option.WithEnableTrace(),
		option.WithNameServer([]string{testBroker}),
		//rocketmq.WithNameServerDomain(testBroker),
	)

	_ = b.Init()

	if err := b.Connect(); err != nil {
		t.Logf("cant connect to broker, skip: %v", err)
		t.Skip()
	}
	defer b.Disconnect()

	var msg api.Hygrothermograph
	const count = 10
	for i := 0; i < count; i++ {
		startTime := time.Now()
		msg.Humidity = float64(rand.Intn(100))
		msg.Temperature = float64(rand.Intn(100))
		err := b.Publish(ctx, testTopic, broker.NewMessage(msg))
		assert.Nil(t, err)
		elapsedTime := time.Since(startTime) / time.Millisecond
		fmt.Printf("Publish %d, elapsed time: %dms, Humidity: %.2f Temperature: %.2f\n",
			i, elapsedTime, msg.Humidity, msg.Temperature)
	}

	fmt.Printf("total send %d messages\n", count)

	<-interrupt
}

func TestAliyunServer(t *testing.T) {
	if testing.Short() {
		t.Skip("manual test: blocks on signal, requires Aliyun credentials")
	}
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	ctx := context.Background()
	endpoint := ""
	accessKey := ""
	secretKey := ""
	instanceId := ""
	topicName := ""
	groupName := "GID_DEFAULT"

	srv := NewServer(
		option.DriverTypeAliyun,
		WithCodec("json"),
		WithEnableTrace(),
		WithNameServerDomain(endpoint),
		WithCredentials(accessKey, secretKey, ""),
		WithInstanceName(instanceId),
		WithGroupName(groupName),
	)

	err := RegisterSubscriber(srv, ctx, topicName, groupName, handleHygrothermograph)
	assert.Nil(t, err)

	require.NoError(t, srv.Start(ctx))

	defer func() {
		if err := srv.Stop(ctx); err != nil {
			t.Errorf("expected nil got %v", err)
		}
	}()

	<-interrupt
}

func TestAliyunClient(t *testing.T) {
	if testing.Short() {
		t.Skip("manual test: blocks on signal, requires Aliyun credentials")
	}
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	ctx := context.Background()

	endpoint := ""
	accessKey := ""
	secretKey := ""
	instanceId := ""
	topicName := ""

	b := rocketmq.NewBroker(broker.WithOptionContext(ctx),
		broker.WithCodec("json"),
		option.WithEnableTrace(),
		option.WithNameServerDomain(endpoint),
		option.WithAccessKey(accessKey),
		option.WithSecretKey(secretKey),
		option.WithInstanceName(instanceId),
	)

	_ = b.Init()

	if err := b.Connect(); err != nil {
		t.Logf("cant connect to broker, skip: %v", err)
		t.Skip()
	}
	defer b.Disconnect()

	var msg api.Hygrothermograph
	const count = 10
	for i := 0; i < count; i++ {
		startTime := time.Now()
		msg.Humidity = float64(rand.Intn(100))
		msg.Temperature = float64(rand.Intn(100))
		err := b.Publish(ctx, topicName, broker.NewMessage(msg))
		assert.Nil(t, err)
		elapsedTime := time.Since(startTime) / time.Millisecond
		fmt.Printf("Publish %d, elapsed time: %dms, Humidity: %.2f Temperature: %.2f\n",
			i, elapsedTime, msg.Humidity, msg.Temperature)
	}

	fmt.Printf("total send %d messages\n", count)

	<-interrupt
}
