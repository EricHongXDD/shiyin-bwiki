package download

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProductionNetworkPolicyBlocksLoopbackBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		_, _ = writer.Write([]byte("不应收到请求"))
	}))
	defer server.Close()

	directory := t.TempDir()
	manager, err := NewManager(Config{StatePath: filepath.Join(directory, "state.json"), Concurrency: 1})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer manager.Close()
	task, err := manager.Add(NewTask{FileName: "blocked.ogg", URL: server.URL, Directory: directory})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	waitTaskStatus(t, manager, task.ID, StatusFailed)
	failed := manager.Get(task.ID)
	if !strings.Contains(failed.Error, "不允许访问本机或内网地址") {
		t.Fatalf("阻断错误 = %q", failed.Error)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("私网阻断前已发出 %d 次请求", got)
	}
}

func TestRestrictedRedirectPolicy(t *testing.T) {
	policy := restrictedRedirectPolicy(nil)
	blocked, _ := url.Parse("http://169.254.169.254/latest/meta-data")
	err := policy(&http.Request{URL: blocked}, []*http.Request{{URL: &url.URL{Scheme: "https", Host: "example.com"}}})
	if err == nil || !strings.Contains(err.Error(), "不允许访问") {
		t.Fatalf("元数据地址重定向未被阻断：%v", err)
	}
	public, _ := url.Parse("https://example.com/audio.ogg")
	if err := policy(&http.Request{URL: public}, nil); err != nil {
		t.Fatalf("公网重定向被错误阻断：%v", err)
	}
}

func TestBlockedAddressClassification(t *testing.T) {
	tests := []struct {
		address string
		blocked bool
	}{
		{address: "127.0.0.1", blocked: true},
		{address: "10.0.0.8", blocked: true},
		{address: "100.64.0.1", blocked: true},
		{address: "169.254.169.254", blocked: true},
		{address: "192.168.1.1", blocked: true},
		{address: "::1", blocked: true},
		{address: "fc00::1", blocked: true},
		{address: "fe80::1", blocked: true},
		{address: "1.1.1.1", blocked: false},
		{address: "2606:4700:4700::1111", blocked: false},
	}
	for _, test := range tests {
		if got := isBlockedAddress(netip.MustParseAddr(test.address)); got != test.blocked {
			t.Errorf("isBlockedAddress(%s) = %v, want %v", test.address, got, test.blocked)
		}
	}
}

func TestResolveHostHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	_, _ = resolveHost(ctx, "example.invalid")
	if time.Since(started) > time.Second {
		t.Fatal("DNS 解析没有及时响应已取消上下文")
	}
}

func TestLiveRestrictedClientCanReachBWikiCDN(t *testing.T) {
	if os.Getenv("BWIKI_LIVE_TEST") != "1" {
		t.Skip("设置 BWIKI_LIVE_TEST=1 后运行真实网络策略验证")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://patchwiki.biligame.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := restrictedHTTPClient(&http.Client{Timeout: 20 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("受限客户端下载 BWIKI CDN 失败：%v", err)
	}
	response.Body.Close()
}
