package download

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBatchIDPersistsAcrossRestart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("voice"))
	}))
	defer server.Close()

	directory := t.TempDir()
	statePath := filepath.Join(directory, "tasks.json")
	manager := newTestManager(t, Config{StatePath: statePath, Concurrency: 1})
	firstBatch, err := manager.AddBatch([]NewTask{
		{FileName: "first-zh.mp3", URL: server.URL + "/zh", Directory: directory},
		{FileName: "first-ja.mp3", URL: server.URL + "/ja", Directory: directory},
	})
	if err != nil {
		t.Fatalf("第一次 AddBatch() error = %v", err)
	}
	secondBatch, err := manager.AddBatch([]NewTask{
		{FileName: "second-en.mp3", URL: server.URL + "/en", Directory: directory},
	})
	if err != nil {
		t.Fatalf("第二次 AddBatch() error = %v", err)
	}
	if firstBatch[0].BatchID == "" || firstBatch[0].BatchID != firstBatch[1].BatchID {
		t.Fatalf("同批任务 BatchID 不一致：%q, %q", firstBatch[0].BatchID, firstBatch[1].BatchID)
	}
	if !firstBatch[0].CreatedAt.Equal(firstBatch[1].CreatedAt) {
		t.Fatalf("同批任务排队时间不一致：%s, %s", firstBatch[0].CreatedAt, firstBatch[1].CreatedAt)
	}
	if secondBatch[0].BatchID == "" || secondBatch[0].BatchID == firstBatch[0].BatchID {
		t.Fatalf("跨批任务 BatchID 未隔离：first=%q, second=%q", firstBatch[0].BatchID, secondBatch[0].BatchID)
	}

	allTasks := append(append([]Task(nil), firstBatch...), secondBatch...)
	waitTasksStatus(t, manager, allTasks, StatusCompleted)
	if err := manager.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened := newTestManager(t, Config{StatePath: statePath, Concurrency: 1})
	defer reopened.Close()
	for _, expected := range allTasks {
		restored, ok := reopened.Lookup(expected.ID)
		if !ok {
			t.Fatalf("重启后缺少任务 %s", expected.ID)
		}
		if restored.BatchID != expected.BatchID {
			t.Fatalf("任务 %s 重启后的 BatchID = %q，期望 %q", expected.ID, restored.BatchID, expected.BatchID)
		}
	}
}

func TestLegacyStateWithoutBatchIDLoads(t *testing.T) {
	directory := t.TempDir()
	statePath := filepath.Join(directory, "tasks.json")
	now := time.Now().UTC()
	legacy := persistedState{
		Version: stateVersion,
		Tasks: []Task{{
			ID:         "legacy-task",
			FileName:   "legacy.mp3",
			URL:        "https://example.com/legacy.mp3",
			Directory:  directory,
			OutputPath: filepath.Join(directory, "legacy.mp3"),
			Status:     StatusCompleted,
			CreatedAt:  now,
			UpdatedAt:  now,
		}},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"batchId"`)) {
		t.Fatalf("兼容性样本意外包含 batchId：%s", data)
	}
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	manager := newTestManager(t, Config{StatePath: statePath, Concurrency: 1})
	restored, ok := manager.Lookup("legacy-task")
	if !ok {
		t.Fatal("旧状态文件中的任务未被加载")
	}
	if restored.BatchID != "" {
		t.Fatalf("旧任务 BatchID = %q，期望保持空值", restored.BatchID)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
