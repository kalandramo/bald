package cron

import (
	"context"
	"log"
	"testing"
	"time"
)

func TestAddRemoveJob(t *testing.T) {
	srv := NewServer()

	// 添加任务
	id1, err := srv.NewTimerJob("*/5 * * * * *", func() {
		log.Println("job 1")
	})
	if err != nil {
		t.Errorf("expected nil got %v", err)
	}

	id2, err := srv.NewTimerJob("*/10 * * * * *", func() {
		log.Println("job 2")
	})
	if err != nil {
		t.Errorf("expected nil got %v", err)
	}

	if srv.GetJobCount() != 2 {
		t.Errorf("expected 2 jobs, got %d", srv.GetJobCount())
	}

	// 移除任务
	srv.RemoveTimerJob(id1)
	if srv.GetJobCount() != 1 {
		t.Errorf("expected 1 job, got %d", srv.GetJobCount())
	}

	// 移除所有
	srv.RemoveAllJobs()
	if srv.GetJobCount() != 0 {
		t.Errorf("expected 0 jobs, got %d", srv.GetJobCount())
	}

	_ = id2
}

func TestServerStartStop(t *testing.T) {
	srv := NewServer()

	_, _ = srv.NewTimerJob("@every 1s", func() {
		// no-op
	})

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(2 * time.Second)
		cancel()
	}()

	if err := srv.Start(ctx); err != nil {
		t.Errorf("expected nil got %v", err)
	}

	log.Println("server started and stopped successfully")
}

func TestEntries(t *testing.T) {
	srv := NewServer()

	_, _ = srv.NewTimerJob("*/5 * * * * *", func() {})
	_, _ = srv.NewTimerJob("*/10 * * * * *", func() {})

	entries := srv.GetEntries()
	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries))
	}

	log.Printf("entries: %+v", entries)
}
