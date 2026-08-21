package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/EricHongXDD/shiyin-bwiki/internal/bwiki"
	"github.com/EricHongXDD/shiyin-bwiki/internal/download"
)

func TestMapParseErrorCodes(t *testing.T) {
	tests := []struct {
		name      string
		parseErr  *bwiki.ParseError
		wantCode  string
		retryable bool
	}{
		{name: "页面不存在", parseErr: &bwiki.ParseError{Kind: bwiki.KindHTTP, StatusCode: http.StatusNotFound, Message: "not found"}, wantCode: "page_not_found"},
		{name: "拒绝访问", parseErr: &bwiki.ParseError{Kind: bwiki.KindHTTP, StatusCode: http.StatusForbidden, Message: "forbidden"}, wantCode: "access_denied"},
		{name: "请求过多", parseErr: &bwiki.ParseError{Kind: bwiki.KindHTTP, StatusCode: http.StatusTooManyRequests, Message: "slow down"}, wantCode: "rate_limited", retryable: true},
		{name: "网络超时", parseErr: &bwiki.ParseError{Kind: bwiki.KindNetwork, Message: "timeout", Err: context.DeadlineExceeded}, wantCode: "timeout", retryable: true},
		{name: "没有音频", parseErr: &bwiki.ParseError{Kind: bwiki.KindNoAudio, Message: "empty"}, wantCode: "no_audio"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mapped := mapParseError(test.parseErr)
			if mapped.Code != test.wantCode || mapped.Retryable != test.retryable {
				t.Fatalf("mapParseError() = %#v", mapped)
			}
		})
	}
}

func TestQueueDownloadsAssignsOneBatchPerCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("voice"))
	}))
	defer server.Close()

	directory := t.TempDir()
	manager, err := download.NewManager(download.Config{
		StatePath:           filepath.Join(directory, "tasks.json"),
		Concurrency:         1,
		AllowPrivateNetwork: true,
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer manager.Close()
	app := &App{downloads: manager, downloadDir: directory}

	first, err := app.QueueDownloads(QueueRequest{Directory: directory, RoleName: "明", Items: []QueueDownloadItem{
		{FileName: "first-zh.mp3", URL: server.URL + "/zh"},
		{FileName: "first-ja.mp3", URL: server.URL + "/ja"},
	}})
	if err != nil {
		t.Fatalf("第一次 QueueDownloads() error = %v", err)
	}
	second, err := app.QueueDownloads(QueueRequest{Directory: directory, Items: []QueueDownloadItem{
		{FileName: "second-en.mp3", URL: server.URL + "/en"},
	}})
	if err != nil {
		t.Fatalf("第二次 QueueDownloads() error = %v", err)
	}
	expectedDirectory := filepath.Join(directory, "明")
	if first[0].Directory != expectedDirectory || first[1].Directory != expectedDirectory {
		t.Fatalf("角色子目录 = %q, %q，期望 %q", first[0].Directory, first[1].Directory, expectedDirectory)
	}
	if first[0].BatchID == "" || first[0].BatchID != first[1].BatchID {
		t.Fatalf("同次调用返回了不同 BatchID：%q, %q", first[0].BatchID, first[1].BatchID)
	}
	if second[0].BatchID == "" || second[0].BatchID == first[0].BatchID {
		t.Fatalf("不同调用未分配独立 BatchID：first=%q, second=%q", first[0].BatchID, second[0].BatchID)
	}
}

func TestApplicationStateDirectoryKeepsLegacyData(t *testing.T) {
	configRoot := t.TempDir()
	preferred := filepath.Join(configRoot, "Shiyin")
	legacy := filepath.Join(configRoot, "BWIKIAudio")

	if got := applicationStateDirectory(configRoot); got != preferred {
		t.Fatalf("新安装的状态目录 = %q，期望 %q", got, preferred)
	}
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatalf("创建旧状态目录失败：%v", err)
	}
	if got := applicationStateDirectory(configRoot); got != legacy {
		t.Fatalf("旧版本兼容目录 = %q，期望 %q", got, legacy)
	}
	if err := os.MkdirAll(preferred, 0o755); err != nil {
		t.Fatalf("创建新状态目录失败：%v", err)
	}
	if got := applicationStateDirectory(configRoot); got != preferred {
		t.Fatalf("新旧目录并存时选择 = %q，期望 %q", got, preferred)
	}
}

func TestRoleNameFromTitle(t *testing.T) {
	tests := []struct {
		title string
		want  string
	}{
		{title: "明/语音台词", want: "明"},
		{title: "米雪儿·李／语音台词", want: "米雪儿·李"},
		{title: "明语音台词", want: "明"},
		{title: "自定义页面", want: "自定义页面"},
	}
	for _, test := range tests {
		t.Run(test.title, func(t *testing.T) {
			if got := roleNameFromTitle(test.title); got != test.want {
				t.Fatalf("roleNameFromTitle(%q) = %q，期望 %q", test.title, got, test.want)
			}
		})
	}
}
