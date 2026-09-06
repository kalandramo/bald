package kafka

import (
	"errors"
	"fmt"
	"net"
	"strconv"

	kafkaGo "github.com/segmentio/kafka-go"
)

func createConnection(addr string) (*kafkaGo.Conn, func(), error) {
	conn, err := kafkaGo.Dial("tcp", addr)
	if err != nil {
		LogErrorf("create kafka connection failed: %s", err.Error())
		return nil, nil, err
	}

	controller, err := conn.Controller()
	if err != nil {
		LogErrorf("create kafka controller failed: %s", err.Error())
		return nil, nil, err
	}

	controllerConn, err := kafkaGo.Dial("tcp", net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port)))
	if err != nil {
		LogErrorf("create kafka controller connection failed: %s", err.Error())
		return nil, nil, err
	}

	return controllerConn, func() {
		if err = conn.Close(); err != nil {
			LogErrorf("failed to close kafka connection: %v", err)
		}
		if err = controllerConn.Close(); err != nil {
			LogErrorf("failed to close kafka controller connection: %v", err)
		}
	}, nil
}

func CreateTopic(addr string, topic string, numPartitions, replicationFactor int) error {
	conn, cleanFunc, err := createConnection(addr)
	if err != nil {
		return fmt.Errorf("create kafka connection failed: %w", err)
	}
	defer cleanFunc()

	err = conn.CreateTopics(kafkaGo.TopicConfig{
		Topic:             topic,
		NumPartitions:     numPartitions,
		ReplicationFactor: replicationFactor,
	})
	if err != nil && errors.Is(err, kafkaGo.TopicAlreadyExists) {
		return nil
	}

	return err
}

func DeleteTopic(addr string, topics ...string) error {
	conn, cleanFunc, err := createConnection(addr)
	if err != nil {
		return fmt.Errorf("create kafka connection failed: %w", err)
	}
	defer cleanFunc()

	return conn.DeleteTopics(topics...)
}
