package download

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDefaultConcurrencyLimit(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		<-release
		_, _ = writer.Write([]byte("ok"))
	}))
	defer server.Close()

	directory := t.TempDir()
	manager := newTestManager(t, Config{StatePath: filepath.Join(directory, "state.json")})
	defer manager.Close()
	inputs := make([]NewTask, 8)
	for index := range inputs {
		inputs[index] = NewTask{
			FileName:  fmt.Sprintf("voice-%d.ogg", index),
			URL:       server.URL,
			Directory: directory,
		}
	}
	tasks, err := manager.AddBatch(inputs)
	if err != nil {
		t.Fatalf("AddBatch() error = %v", err)
	}
	waitFor(t, 3*time.Second, func() bool { return maximum.Load() == defaultConcurrency }, "默认并发数未达到 4")
	if got := maximum.Load(); got > defaultConcurrency {
		t.Fatalf("最大并发数 = %d，期望不超过 %d", got, defaultConcurrency)
	}
	close(release)
	waitTasksStatus(t, manager, tasks, StatusCompleted)
}

func TestRangeResumeAndIfRange(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 4096)
	cut := len(data) / 3
	var requests atomic.Int32
	var seenRange, seenIfRange string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		call := requests.Add(1)
		writer.Header().Set("ETag", `"voice-v1"`)
		if call == 1 {
			writer.Header().Set("Content-Length", strconv.Itoa(len(data)))
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write(data[:cut])
			return
		}
		seenRange = request.Header.Get("Range")
		seenIfRange = request.Header.Get("If-Range")
		writer.Header().Set("Content-Length", strconv.Itoa(len(data)-cut))
		writer.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", cut, len(data)-1, len(data)))
		writer.WriteHeader(http.StatusPartialContent)
		_, _ = writer.Write(data[cut:])
	}))
	defer server.Close()

	directory := t.TempDir()
	manager := newTestManager(t, Config{StatePath: filepath.Join(directory, "state.json"), Concurrency: 1})
	defer manager.Close()
	task, err := manager.Add(NewTask{FileName: "voice.ogg", URL: server.URL, Directory: directory})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	waitTaskStatus(t, manager, task.ID, StatusFailed)
	failed := manager.Get(task.ID)
	if failed.Bytes != int64(cut) {
		t.Fatalf("首次部分长度 = %d，期望 %d", failed.Bytes, cut)
	}
	if err := manager.Retry(task.ID); err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	waitTaskStatus(t, manager, task.ID, StatusCompleted)
	if seenRange != fmt.Sprintf("bytes=%d-", cut) {
		t.Fatalf("Range = %q", seenRange)
	}
	if seenIfRange != `"voice-v1"` {
		t.Fatalf("If-Range = %q", seenIfRange)
	}
	assertFileContent(t, task.OutputPath, data)
	if _, err := os.Stat(task.OutputPath + ".part"); !os.IsNotExist(err) {
		t.Fatalf("完成后 .part 仍然存在：%v", err)
	}
}

func TestIgnoredRangeRestartsSafely(t *testing.T) {
	oldData := bytes.Repeat([]byte("old-"), 4096)
	newData := bytes.Repeat([]byte("new-representation-"), 2048)
	cut := len(oldData) / 2
	var requests atomic.Int32
	var resumedRange string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if requests.Add(1) == 1 {
			writer.Header().Set("ETag", `"old"`)
			writer.Header().Set("Content-Length", strconv.Itoa(len(oldData)))
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write(oldData[:cut])
			return
		}
		resumedRange = request.Header.Get("Range")
		writer.Header().Set("ETag", `"new"`)
		writer.Header().Set("Content-Length", strconv.Itoa(len(newData)))
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(newData)
	}))
	defer server.Close()

	directory := t.TempDir()
	manager := newTestManager(t, Config{StatePath: filepath.Join(directory, "state.json"), Concurrency: 1})
	defer manager.Close()
	task, err := manager.Add(NewTask{FileName: "changed.ogg", URL: server.URL, Directory: directory})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	waitTaskStatus(t, manager, task.ID, StatusFailed)
	if err := manager.Retry(task.ID); err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	waitTaskStatus(t, manager, task.ID, StatusCompleted)
	if resumedRange != fmt.Sprintf("bytes=%d-", cut) {
		t.Fatalf("Range = %q", resumedRange)
	}
	assertFileContent(t, task.OutputPath, newData)
}

func TestConflictingValidatorNeverAppends(t *testing.T) {
	oldData := bytes.Repeat([]byte("old-segment-"), 2048)
	newData := bytes.Repeat([]byte("new-resource-"), 3072)
	cut := len(oldData) / 2
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch requests.Add(1) {
		case 1:
			writer.Header().Set("ETag", `"old"`)
			writer.Header().Set("Content-Length", strconv.Itoa(len(oldData)))
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write(oldData[:cut])
		case 2:
			if got := request.Header.Get("Range"); got != fmt.Sprintf("bytes=%d-", cut) {
				t.Errorf("第二次请求 Range = %q", got)
			}
			writer.Header().Set("ETag", `"new"`)
			writer.Header().Set("Content-Length", strconv.Itoa(len(newData)-cut))
			writer.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", cut, len(newData)-1, len(newData)))
			writer.WriteHeader(http.StatusPartialContent)
			_, _ = writer.Write(newData[cut:])
		default:
			if got := request.Header.Get("Range"); got != "" {
				t.Errorf("校验器冲突后的请求仍携带 Range：%q", got)
			}
			writer.Header().Set("ETag", `"new"`)
			writer.Header().Set("Content-Length", strconv.Itoa(len(newData)))
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write(newData)
		}
	}))
	defer server.Close()

	directory := t.TempDir()
	manager := newTestManager(t, Config{StatePath: filepath.Join(directory, "state.json"), Concurrency: 1})
	defer manager.Close()
	task, err := manager.Add(NewTask{FileName: "validator.ogg", URL: server.URL, Directory: directory})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	waitTaskStatus(t, manager, task.ID, StatusFailed)
	if err := manager.Retry(task.ID); err != nil {
		t.Fatalf("第一次 Retry() error = %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		current := manager.Get(task.ID)
		return current.Status == StatusFailed && current.Bytes == 0 && current.ETag == `"new"`
	}, "校验器冲突后任务没有安全归零")
	partInfo, err := os.Stat(task.OutputPath + ".part")
	if err != nil {
		t.Fatalf("检查归零后的 .part：%v", err)
	}
	if partInfo.Size() != 0 {
		t.Fatalf("校验器冲突后的 .part 长度 = %d", partInfo.Size())
	}
	if err := manager.Retry(task.ID); err != nil {
		t.Fatalf("第二次 Retry() error = %v", err)
	}
	waitTaskStatus(t, manager, task.ID, StatusCompleted)
	assertFileContent(t, task.OutputPath, newData)
}

func TestRange416CompletesMatchingPartial(t *testing.T) {
	data := bytes.Repeat([]byte("already-complete-"), 1024)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("ETag", `"voice-v1"`)
		if requests.Add(1) == 1 {
			// 故意多声明一个字节，模拟文件写全后连接异常中断。
			writer.Header().Set("Content-Length", strconv.Itoa(len(data)+1))
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write(data)
			return
		}
		if got := request.Header.Get("Range"); got != fmt.Sprintf("bytes=%d-", len(data)) {
			t.Errorf("Range = %q", got)
		}
		writer.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(data)))
		writer.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer server.Close()

	directory := t.TempDir()
	manager := newTestManager(t, Config{StatePath: filepath.Join(directory, "state.json"), Concurrency: 1})
	defer manager.Close()
	task, err := manager.Add(NewTask{FileName: "complete-part.ogg", URL: server.URL, Directory: directory})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	waitTaskStatus(t, manager, task.ID, StatusFailed)
	if err := manager.Retry(task.ID); err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	waitTaskStatus(t, manager, task.ID, StatusCompleted)
	assertFileContent(t, task.OutputPath, data)
}

func TestPauseThenResume(t *testing.T) {
	data := bytes.Repeat([]byte("voice-data-"), 64*1024)
	var sawResume atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("ETag", `"voice-v1"`)
		start := 0
		if header := request.Header.Get("Range"); header != "" {
			value := strings.TrimSuffix(strings.TrimPrefix(header, "bytes="), "-")
			parsed, err := strconv.Atoi(value)
			if err != nil {
				http.Error(writer, "bad range", http.StatusBadRequest)
				return
			}
			start = parsed
			sawResume.Store(true)
			writer.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(data)-1, len(data)))
			writer.WriteHeader(http.StatusPartialContent)
		} else {
			writer.Header().Set("Content-Length", strconv.Itoa(len(data)))
		}
		flusher, _ := writer.(http.Flusher)
		for position := start; position < len(data); position += 4096 {
			end := position + 4096
			if end > len(data) {
				end = len(data)
			}
			if _, err := writer.Write(data[position:end]); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			if start == 0 {
				time.Sleep(3 * time.Millisecond)
			}
		}
	}))
	defer server.Close()

	directory := t.TempDir()
	manager := newTestManager(t, Config{
		StatePath:        filepath.Join(directory, "state.json"),
		Concurrency:      1,
		ProgressInterval: 10 * time.Millisecond,
	})
	defer manager.Close()
	task, err := manager.Add(NewTask{FileName: "pausable.ogg", URL: server.URL, Directory: directory})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	waitFor(t, 3*time.Second, func() bool {
		current := manager.Get(task.ID)
		return current.Status == StatusDownloading && current.Bytes >= 4096
	}, "下载未产生可暂停的进度")
	if err := manager.Pause(task.ID); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	if got := manager.Get(task.ID).Status; got != StatusPaused {
		t.Fatalf("暂停后状态 = %s", got)
	}
	if err := manager.Resume(task.ID); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	waitTaskStatus(t, manager, task.ID, StatusCompleted)
	if !sawResume.Load() {
		t.Fatal("恢复下载未发送 Range 请求")
	}
	assertFileContent(t, task.OutputPath, data)
}

func TestPersistenceRecoveryAndRepeatedReplace(t *testing.T) {
	directory := t.TempDir()
	statePath := filepath.Join(directory, "tasks.json")
	now := time.Now().UTC()
	state := persistedState{
		Version: stateVersion,
		Tasks: []Task{
			{
				ID:         "active-before-restart",
				FileName:   "voice.ogg",
				URL:        "https://example.invalid/voice.ogg",
				Directory:  directory,
				OutputPath: filepath.Join(directory, "voice.ogg"),
				Status:     StatusDownloading,
				Bytes:      123,
				Total:      456,
				Speed:      789,
				CreatedAt:  now,
				UpdatedAt:  now,
			},
		},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	manager := newTestManager(t, Config{StatePath: statePath, Concurrency: 1})
	restored := manager.Get("active-before-restart")
	if restored.Status != StatusPaused || restored.Speed != 0 {
		t.Fatalf("恢复状态 = %s, speed = %f", restored.Status, restored.Speed)
	}
	if err := manager.persist(); err != nil {
		t.Fatalf("第一次连续 persist() error = %v", err)
	}
	if err := manager.persist(); err != nil {
		t.Fatalf("第二次连续 persist() error = %v", err)
	}
	if _, err := os.Stat(statePath + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("成功替换后备份文件仍存在：%v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened := newTestManager(t, Config{StatePath: statePath, Concurrency: 1})
	defer reopened.Close()
	if got := reopened.Get("active-before-restart").Status; got != StatusPaused {
		t.Fatalf("再次打开后状态 = %s", got)
	}
}

func TestDuplicateFileNames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("voice"))
	}))
	defer server.Close()
	directory := t.TempDir()
	manager := newTestManager(t, Config{StatePath: filepath.Join(directory, "state.json"), Concurrency: 2})
	defer manager.Close()
	tasks, err := manager.AddBatch([]NewTask{
		{FileName: `../CON?.ogg`, URL: server.URL, Directory: directory},
		{FileName: `../CON?.ogg`, URL: server.URL, Directory: directory},
	})
	if err != nil {
		t.Fatalf("AddBatch() error = %v", err)
	}
	if tasks[0].FileName == tasks[1].FileName || tasks[0].OutputPath == tasks[1].OutputPath {
		t.Fatalf("重名任务未分配唯一名称：%q, %q", tasks[0].FileName, tasks[1].FileName)
	}
	for _, task := range tasks {
		if filepath.Dir(task.OutputPath) != directory {
			t.Fatalf("输出路径逃逸下载目录：%s", task.OutputPath)
		}
	}
	waitTasksStatus(t, manager, tasks, StatusCompleted)
}

func newTestManager(t *testing.T, config Config) *Manager {
	t.Helper()
	// 单元测试使用 httptest 回环地址；生产配置不会开启此选项。
	config.AllowPrivateNetwork = true
	manager, err := NewManager(config)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return manager
}

func waitTasksStatus(t *testing.T, manager *Manager, tasks []Task, status Status) {
	t.Helper()
	waitFor(t, 5*time.Second, func() bool {
		for _, task := range tasks {
			if manager.Get(task.ID).Status != status {
				return false
			}
		}
		return true
	}, fmt.Sprintf("任务未全部进入 %s", status))
}

func waitTaskStatus(t *testing.T, manager *Manager, id string, status Status) {
	t.Helper()
	waitFor(t, 5*time.Second, func() bool { return manager.Get(id).Status == status }, fmt.Sprintf("任务 %s 未进入 %s", id, status))
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(message)
}

func assertFileContent(t *testing.T, path string, expected []byte) {
	t.Helper()
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取下载文件失败：%v", err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("下载文件内容不一致：got %d bytes, want %d bytes", len(actual), len(expected))
	}
}
