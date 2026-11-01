package asynq

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/kalandramo/bald/encoding"
	baldjson "github.com/kalandramo/bald/encoding/json"
	"github.com/stretchr/testify/require"
)

// TestMain 注册 JSON codec，任务载荷编解码依赖它。
func TestMain(m *testing.M) {
	encoding.MustRegister(baldjson.New())
	os.Exit(m.Run())
}

// defaultTestRedisURI 是本机联调默认值；CI 或无 Redis 环境请设置
// BALD_TEST_REDIS_URI，测试在地址不可达时会自动跳过。
const defaultTestRedisURI = "redis://:*Abcd123456@127.0.0.1:6379"

const (
	testTask1        = "test_task_1"
	testDelayTask    = "test_delay_task"
	testPeriodicTask = "test_periodic_task"
)

// TaskPayload 是集成测试用的通用载荷。
type TaskPayload struct {
	Message string `json:"message"`
}

// testRedisURI 返回集成测试使用的 Redis 连接串。
func testRedisURI() string {
	if uri := os.Getenv("BALD_TEST_REDIS_URI"); uri != "" {
		return uri
	}
	return defaultTestRedisURI
}

// requireRedis 校验 Redis 可用性：-short 模式或地址不可达时跳过测试。
func requireRedis(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	uri := testRedisURI()
	host := redisHost(uri)
	if host == "" {
		t.Skipf("cannot determine Redis host from BALD_TEST_REDIS_URI=%q", uri)
	}
	conn, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		t.Skipf("Redis unreachable at %s (set BALD_TEST_REDIS_URI to run): %v", host, err)
	}
	_ = conn.Close()
	return uri
}

// redisHost 从连接串提取首个地址，兼容 redis+cluster 的逗号列表。
func redisHost(uri string) string {
	if i := strings.Index(uri, "://"); i >= 0 {
		uri = uri[i+3:]
	}
	if i := strings.Index(uri, "/"); i >= 0 {
		uri = uri[:i]
	}
	if i := strings.LastIndex(uri, "@"); i >= 0 {
		uri = uri[i+1:]
	}
	if i := strings.Index(uri, ","); i >= 0 {
		uri = uri[:i]
	}
	return uri
}

// testQueue 生成每次运行独有的队列名，避免共享 Redis 上残留任务串扰。
func testQueue(prefix string) string {
	return fmt.Sprintf("bald_test:%s:%d", prefix, time.Now().UnixNano())
}

// newTestServer 构造只消费指定队列的服务器，隔离并发测试。
func newTestServer(t *testing.T, queue string) *Server {
	t.Helper()
	return NewServer(
		WithRedisURI(requireRedis(t)),
		WithQueues(map[string]int32{queue: 1}),
		WithShutdownTimeout(3*time.Second),
	)
}

// startTestServer 在后台启动服务器，测试结束时取消 ctx 并等待退出。
func startTestServer(t *testing.T, srv *Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Start returned error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("server did not stop within 5s after ctx cancellation")
		}
	})
}

// waitTaskType 等待某个任务类型被消费。
func waitTaskType(t *testing.T, handled <-chan string, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case got := <-handled:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out after %v waiting for task %q", timeout, want)
		}
	}
}

func TestNewTaskOnly(t *testing.T) {
	queue := testQueue("task")
	srv := newTestServer(t, queue)

	handled := make(chan string, 1)
	require.NoError(t, RegisterSubscriber(srv, testTask1, func(string, *TaskPayload) error {
		handled <- "ok"
		return nil
	}))
	require.NoError(t, srv.NewTask(testTask1,
		&TaskPayload{Message: "immediate task"},
		asynq.Queue(queue),
	))

	startTestServer(t, srv)
	waitTaskType(t, handled, "ok", 5*time.Second)
}

func TestNewPeriodicTaskOnly(t *testing.T) {
	srv := NewServer(WithShutdownTimeout(time.Second))
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	entryID, err := srv.NewPeriodicTask("@every 1s", testPeriodicTask,
		&TaskPayload{Message: "periodic task"},
	)
	require.NoError(t, err)
	require.NotEmpty(t, entryID)
	require.Equal(t, entryID, srv.QueryPeriodicTaskEntryID(testPeriodicTask))

	require.NoError(t, srv.RemovePeriodicTask(testPeriodicTask))
	require.Empty(t, srv.QueryPeriodicTaskEntryID(testPeriodicTask))
}

func TestDelayTask(t *testing.T) {
	queue := testQueue("delay")
	srv := newTestServer(t, queue)

	executed := make(chan time.Time, 1)
	require.NoError(t, RegisterSubscriber(srv, testDelayTask, func(string, *TaskPayload) error {
		executed <- time.Now()
		return nil
	}))

	enqueuedAt := time.Now()
	const delay = 500 * time.Millisecond
	require.NoError(t, srv.NewTask(testDelayTask,
		&TaskPayload{Message: "delayed task"},
		asynq.Queue(queue),
		asynq.ProcessIn(delay),
	))

	startTestServer(t, srv)
	select {
	case at := <-executed:
		if elapsed := at.Sub(enqueuedAt); elapsed < delay/2 {
			t.Fatalf("task executed after %v, want at least %v", elapsed, delay/2)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delayed task was not executed within 5s")
	}
}

func TestPeriodicTask(t *testing.T) {
	queue := testQueue("periodic")
	srv := newTestServer(t, queue)

	handled := make(chan string, 4)
	require.NoError(t, RegisterSubscriber(srv, testPeriodicTask, func(taskType string, _ *TaskPayload) error {
		handled <- taskType
		return nil
	}))
	_, err := srv.NewPeriodicTask("@every 1s", testPeriodicTask,
		&TaskPayload{Message: "periodic task"},
		asynq.Queue(queue),
	)
	require.NoError(t, err)

	startTestServer(t, srv)
	waitTaskType(t, handled, testPeriodicTask, 5*time.Second)
}

func TestTaskSubscribe(t *testing.T) {
	queue := testQueue("subscribe")
	srv := newTestServer(t, queue)

	handled := make(chan string, 4)
	for _, taskType := range []string{testTask1, testDelayTask, testPeriodicTask} {
		require.NoError(t, RegisterSubscriber(srv, taskType, func(taskType string, _ *TaskPayload) error {
			handled <- taskType
			return nil
		}))
		require.True(t, srv.TaskTypeExists(taskType), "task type %q should be registered", taskType)
	}
	require.ElementsMatch(t,
		[]string{testTask1, testDelayTask, testPeriodicTask},
		srv.GetRegisteredTaskTypes(),
	)

	require.NoError(t, srv.NewTask(testTask1, &TaskPayload{Message: "one"}, asynq.Queue(queue)))
	require.NoError(t, srv.NewTask(testDelayTask, &TaskPayload{Message: "two"},
		asynq.Queue(queue), asynq.ProcessIn(100*time.Millisecond),
	))

	startTestServer(t, srv)
	waitTaskType(t, handled, testTask1, 5*time.Second)
	waitTaskType(t, handled, testDelayTask, 5*time.Second)
}

func TestAllInOne(t *testing.T) {
	queue := testQueue("all")
	srv := newTestServer(t, queue)

	handled := make(chan string, 8)
	for _, taskType := range []string{testTask1, testDelayTask, testPeriodicTask} {
		require.NoError(t, RegisterSubscriber(srv, taskType, func(taskType string, _ *TaskPayload) error {
			handled <- taskType
			return nil
		}))
	}

	require.NoError(t, srv.NewTask(testTask1, &TaskPayload{Message: "immediate"},
		asynq.Queue(queue), asynq.MaxRetry(3), asynq.Timeout(10*time.Second),
	))
	require.NoError(t, srv.NewTask(testDelayTask, &TaskPayload{Message: "delayed"},
		asynq.Queue(queue), asynq.ProcessIn(500*time.Millisecond),
	))
	_, err := srv.NewPeriodicTask("@every 1s", testPeriodicTask,
		&TaskPayload{Message: "periodic"}, asynq.Queue(queue),
	)
	require.NoError(t, err)

	startTestServer(t, srv)
	for _, want := range []string{testTask1, testDelayTask, testPeriodicTask} {
		waitTaskType(t, handled, want, 8*time.Second)
	}
}

func TestWaitResultTask(t *testing.T) {
	queue := testQueue("wait")
	srv := newTestServer(t, queue)

	require.NoError(t, RegisterSubscriber(srv, testTask1, func(string, *TaskPayload) error {
		return nil
	}))
	startTestServer(t, srv)

	err := srv.NewWaitResultTask(testTask1,
		&TaskPayload{Message: "wait result task"},
		asynq.Queue(queue),
		asynq.Retention(time.Hour),
		asynq.MaxRetry(1),
		asynq.Timeout(10*time.Second),
	)
	require.NoError(t, err)
}
