package jobs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func waitStatus(t *testing.T, m *Manager, id, want string) *Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, ok := m.Get(id)
		if ok && j.Status == want {
			return j
		}
		time.Sleep(20 * time.Millisecond)
	}
	j, _ := m.Get(id)
	t.Fatalf("任务未达到状态 %s，当前 %+v", want, j)
	return nil
}

func TestJobLifecycleAndPersistence(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	m.Register(TypeTranscribe, func(ctx context.Context, sink *Sink, job *Job) (*Result, error) {
		sink.Progress(0.2, "开始")
		sink.Logf("处理中")
		time.Sleep(60 * time.Millisecond)
		sink.Progress(0.8, "快好了")
		return &Result{CueCount: 3, Message: "ok"}, nil
	})
	job, err := m.Create(TypeTranscribe, "p1", "测试任务", map[string]any{"format": "srt"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(job.ID); err != nil {
		t.Fatal(err)
	}
	done := waitStatus(t, m, job.ID, StatusSucceeded)
	if done.Result == nil || done.Result.CueCount != 3 {
		t.Fatalf("任务结果错误：%+v", done.Result)
	}
	if done.Overall != 1 {
		t.Fatalf("完成后进度应为 1，实际 %v", done.Overall)
	}
	if !m.WaitIdle(3 * time.Second) { t.Fatal("任务未结束") }
	if len(m.Logs(job.ID, 10)) == 0 {
		t.Fatal("日志未记录")
	}

	// 重新加载应能读到历史任务。
	m2, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded, ok := m2.Get(job.ID)
	if !ok || loaded.Status != StatusSucceeded {
		t.Fatalf("历史任务丢失：%+v", loaded)
	}
}

func TestCancelAndFailure(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)
	m.Register(TypeTranscode, func(ctx context.Context, sink *Sink, job *Job) (*Result, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return &Result{}, nil
		}
	})
	job, _ := m.Create(TypeTranscode, "p1", "取消测试", nil)
	_ = m.Start(job.ID)
	time.Sleep(50 * time.Millisecond)
	if err := m.Cancel(job.ID); err != nil {
		t.Fatalf("取消失败：%v", err)
	}
	waitStatus(t, m, job.ID, StatusCanceled)

	m.Register(TypeExport, func(ctx context.Context, sink *Sink, job *Job) (*Result, error) {
		return nil, os.ErrNotExist
	})
	job2, _ := m.Create(TypeExport, "p1", "失败测试", nil)
	_ = m.Start(job2.ID)
	waitStatus(t, m, job2.ID, StatusFailed)
}

func TestInterruptedJobMarked(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)
	job, _ := m.Create(TypeTranscribe, "p1", "中断", nil)
	// 手工把状态改成 running，模拟进程被杀。
	j, _ := m.Get(job.ID)
	j.Status = StatusRunning
	m.mu.Lock()
	m.jobs[job.ID] = j
	m.mu.Unlock()
	m.persist(j)

	m2, _ := NewManager(dir)
	loaded, ok := m2.Get(job.ID)
	if !ok {
		t.Fatal("任务丢失")
	}
	if loaded.Status != StatusInterrupted || !loaded.Resumable {
		t.Fatalf("中断任务状态错误：%+v", loaded)
	}
}

var _ = filepath.Join

