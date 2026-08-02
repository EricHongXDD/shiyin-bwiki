package download

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPersistFailureStillAppliesTaskSideEffects(t *testing.T) {
	blockedParent := filepath.Join(t.TempDir(), "state-parent")
	if err := os.WriteFile(blockedParent, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	pauseContext, pauseCancel := context.WithCancel(context.Background())
	cancelContext, cancelCancel := context.WithCancel(context.Background())
	defer pauseCancel()
	defer cancelCancel()
	updates := make([]Task, 0, 4)
	manager := &Manager{
		tasks: map[string]*taskState{
			"pause": {
				task:   Task{ID: "pause", Status: StatusDownloading},
				active: true,
				cancel: pauseCancel,
			},
			"resume": {task: Task{ID: "resume", Status: StatusPaused}},
			"retry":  {task: Task{ID: "retry", Status: StatusFailed}},
			"cancel": {
				task:   Task{ID: "cancel", Status: StatusDownloading},
				active: true,
				cancel: cancelCancel,
			},
		},
		statePath: filepath.Join(blockedParent, "tasks.json"),
		onUpdate: func(task Task) {
			updates = append(updates, task)
		},
	}
	manager.cond = sync.NewCond(&manager.mu)

	// 状态文件无法创建时，命令仍须完成与已接受状态一致的副作用。
	if err := manager.Pause("pause"); err == nil {
		t.Fatal("Pause() 在持久化失败时未返回错误")
	}
	if err := manager.Resume("resume"); err == nil {
		t.Fatal("Resume() 在持久化失败时未返回错误")
	}
	if err := manager.Retry("retry"); err == nil {
		t.Fatal("Retry() 在持久化失败时未返回错误")
	}
	if err := manager.Cancel("cancel"); err == nil {
		t.Fatal("Cancel() 在持久化失败时未返回错误")
	}

	select {
	case <-pauseContext.Done():
	default:
		t.Fatal("Pause() 未取消正在运行的下载上下文")
	}
	select {
	case <-cancelContext.Done():
	default:
		t.Fatal("Cancel() 未取消正在运行的下载上下文")
	}

	if status := manager.Get("pause").Status; status != StatusPaused {
		t.Fatalf("Pause() 后状态 = %s，期望 %s", status, StatusPaused)
	}
	if status := manager.Get("resume").Status; status != StatusQueued {
		t.Fatalf("Resume() 后状态 = %s，期望 %s", status, StatusQueued)
	}
	if status := manager.Get("retry").Status; status != StatusQueued {
		t.Fatalf("Retry() 后状态 = %s，期望 %s", status, StatusQueued)
	}
	if status := manager.Get("cancel").Status; status != StatusCanceled {
		t.Fatalf("Cancel() 后状态 = %s，期望 %s", status, StatusCanceled)
	}

	manager.mu.RLock()
	pending := append([]string(nil), manager.pending...)
	manager.mu.RUnlock()
	if len(pending) != 2 || pending[0] != "resume" || pending[1] != "retry" {
		t.Fatalf("持久化失败后的待下载队列 = %v，期望 [resume retry]", pending)
	}
	if len(updates) != 4 {
		t.Fatalf("持久化失败后的任务通知数 = %d，期望 4", len(updates))
	}
}
