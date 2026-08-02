package download

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestWeakETagWithoutDateRestartsSafely(t *testing.T) {
	oldData := bytes.Repeat([]byte("old-version-"), 2048)
	newData := bytes.Repeat([]byte("new-version-"), 3072)
	cut := len(oldData) / 2
	var requests atomic.Int32
	var retryRange atomic.Value
	var retryIfRange atomic.Value

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if requests.Add(1) == 1 {
			writer.Header().Set("ETag", `W/"old"`)
			writer.Header().Set("Content-Length", strconv.Itoa(len(oldData)))
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write(oldData[:cut])
			return
		}
		retryRange.Store(request.Header.Get("Range"))
		retryIfRange.Store(request.Header.Get("If-Range"))
		writer.Header().Set("ETag", `W/"new"`)
		writer.Header().Set("Content-Length", strconv.Itoa(len(newData)))
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(newData)
	}))
	defer server.Close()

	directory := t.TempDir()
	manager := newTestManager(t, Config{StatePath: filepath.Join(directory, "state.json"), Concurrency: 1})
	defer manager.Close()
	task, err := manager.Add(NewTask{FileName: "weak-etag.ogg", URL: server.URL, Directory: directory})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	waitTaskStatus(t, manager, task.ID, StatusFailed)
	failed := manager.Get(task.ID)
	if failed.Bytes != int64(cut) || failed.ETag != `W/"old"` {
		t.Fatalf("首次分段状态异常：bytes=%d, etag=%q", failed.Bytes, failed.ETag)
	}
	if err := manager.Retry(task.ID); err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	waitTaskStatus(t, manager, task.ID, StatusCompleted)
	if value, _ := retryRange.Load().(string); value != "" {
		t.Fatalf("仅有弱 ETag 时不应续传，Range = %q", value)
	}
	if value, _ := retryIfRange.Load().(string); value != "" {
		t.Fatalf("仅有弱 ETag 时不应发送 If-Range，实际 = %q", value)
	}
	assertFileContent(t, task.OutputPath, newData)
}

func TestRecoverRenamedDownloadAfterCompletionCheckpoint(t *testing.T) {
	directory := t.TempDir()
	outputPath := filepath.Join(directory, "voice.ogg")
	data := bytes.Repeat([]byte("complete-voice-"), 256)
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	task := Task{
		OutputPath: outputPath,
		Bytes:      int64(len(data)),
		Total:      int64(len(data)),
	}

	completed, knownBytes, err := recoverRenamedDownload(task, outputPath+".part")
	if err != nil {
		t.Fatalf("recoverRenamedDownload() error = %v", err)
	}
	if !completed || knownBytes != int64(len(data)) {
		t.Fatalf("恢复结果 = completed:%v bytes:%d", completed, knownBytes)
	}
}

func TestRestartCompletesFileRenamedAfterCheckpoint(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		http.Error(writer, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()

	directory := t.TempDir()
	outputPath := filepath.Join(directory, "voice.ogg")
	statePath := filepath.Join(directory, "tasks.json")
	data := bytes.Repeat([]byte("checkpointed-voice-"), 256)
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	task := Task{
		ID:         "renamed-after-checkpoint",
		FileName:   filepath.Base(outputPath),
		URL:        server.URL + "/voice.ogg",
		Directory:  directory,
		OutputPath: outputPath,
		Status:     StatusDownloading,
		Bytes:      int64(len(data)),
		Total:      int64(len(data)),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	stateData, err := json.Marshal(persistedState{Version: stateVersion, Tasks: []Task{task}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, stateData, 0o600); err != nil {
		t.Fatal(err)
	}

	manager := newTestManager(t, Config{StatePath: statePath, Concurrency: 1})
	defer manager.Close()
	if got := manager.Get(task.ID).Status; got != StatusPaused {
		t.Fatalf("重启后的任务状态 = %s", got)
	}
	if err := manager.Resume(task.ID); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	waitTaskStatus(t, manager, task.ID, StatusCompleted)
	if got := requests.Load(); got != 0 {
		t.Fatalf("恢复已改名文件时发出了 %d 次多余网络请求", got)
	}
}

func TestStrongETagDetection(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: `"voice-v1"`, want: true},
		{value: ` W/"voice-v1" `, want: false},
		{value: `w/"voice-v1"`, want: false},
		{value: "voice-v1", want: false},
		{value: "", want: false},
	} {
		if got := isStrongETag(test.value); got != test.want {
			t.Errorf("isStrongETag(%q) = %v, want %v", test.value, got, test.want)
		}
	}
}
