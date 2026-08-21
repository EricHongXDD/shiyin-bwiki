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

func TestTextTaskWritesUTF8IntoSubdirectory(t *testing.T) {
	directory := t.TempDir()
	manager := newTestManager(t, Config{StatePath: filepath.Join(directory, "state.json"), Concurrency: 1})
	defer manager.Close()

	content := "你好，明。\r\n这是对应的语音台词。"
	task, err := manager.Add(NewTask{
		Type:         TaskTypeText,
		FileName:     "明语音-022CN.txt",
		Directory:    directory,
		Subdirectory: "明",
		Content:      content,
	})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	waitTaskStatus(t, manager, task.ID, StatusCompleted)

	expectedDirectory := filepath.Join(directory, "明")
	if task.Directory != expectedDirectory {
		t.Fatalf("任务目录 = %q，期望 %q", task.Directory, expectedDirectory)
	}
	data, err := os.ReadFile(filepath.Join(expectedDirectory, "明语音-022CN.txt"))
	if err != nil {
		t.Fatalf("读取字幕文件失败：%v", err)
	}
	if string(data) != content {
		t.Fatalf("字幕内容 = %q，期望 %q", string(data), content)
	}
	if _, err := os.Stat(task.OutputPath + ".part"); !os.IsNotExist(err) {
		t.Fatalf("字幕临时文件未清理：err=%v", err)
	}
}
func TestCompletedTaskCanReuseNameAfterFileDeletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("voice"))
	}))
	defer server.Close()
	directory := t.TempDir()
	manager := newTestManager(t, Config{StatePath: filepath.Join(directory, "state.json"), Concurrency: 1})
	defer manager.Close()
	first, err := manager.Add(NewTask{
		FileName:  "明语音-022CN.mp3",
		URL:       server.URL,
		Directory: directory,
	})
	if err != nil {
		t.Fatalf("第一次 Add() error = %v", err)
	}
	waitTaskStatus(t, manager, first.ID, StatusCompleted)
	if err := os.Remove(first.OutputPath); err != nil {
		t.Fatalf("删除旧音频文件失败：%v", err)
	}
	second, err := manager.Add(NewTask{
		FileName:  "明语音-022CN.mp3",
		URL:       server.URL,
		Directory: directory,
	})
	if err != nil {
		t.Fatalf("删除旧文件后再次 Add() error = %v", err)
	}
	if second.FileName != "明语音-022CN.mp3" {
		t.Fatalf("删除旧文件后错误生成重复文件名：%q", second.FileName)
	}
	waitTaskStatus(t, manager, second.ID, StatusCompleted)
}

func TestAddSubtitleTaskForCompletedAudio(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("voice"))
	}))
	defer server.Close()

	directory := t.TempDir()
	manager := newTestManager(t, Config{StatePath: filepath.Join(directory, "state.json"), Concurrency: 1})
	defer manager.Close()

	voice, err := manager.Add(NewTask{
		SourceID:     "voice-022",
		Title:        "你好，明。",
		Category:     "日常",
		Language:     "zh-CN",
		LanguageName: "中文",
		FileName:     "明语音-022CN.mp3",
		URL:          server.URL,
		Directory:    directory,
		Content:      "你好，明。",
	})
	if err != nil {
		t.Fatalf("音频 Add() error = %v", err)
	}
	waitTaskStatus(t, manager, voice.ID, StatusCompleted)

	subtitle, err := manager.AddSubtitleTask(voice.ID)
	if err != nil {
		t.Fatalf("AddSubtitleTask() error = %v", err)
	}
	if subtitle.Type != TaskTypeText || subtitle.FileName != "明语音-022CN.txt" {
		t.Fatalf("字幕任务 = %#v", subtitle)
	}
	if subtitle.Directory != voice.Directory {
		t.Fatalf("字幕目录 = %q，期望与音频目录 %q 一致", subtitle.Directory, voice.Directory)
	}
	waitTaskStatus(t, manager, subtitle.ID, StatusCompleted)
	data, err := os.ReadFile(subtitle.OutputPath)
	if err != nil {
		t.Fatalf("读取补下载字幕失败：%v", err)
	}
	if string(data) != voice.Content {
		t.Fatalf("字幕内容 = %q，期望 %q", string(data), voice.Content)
	}
	if _, err := manager.AddSubtitleTask(voice.ID); err == nil {
		t.Fatal("重复补下载字幕未返回错误")
	}
}
