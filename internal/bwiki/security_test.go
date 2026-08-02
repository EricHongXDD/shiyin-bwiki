package bwiki

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestBWikiRedirectPolicy(t *testing.T) {
	policy := bwikiRedirectPolicy(nil)
	for _, rawURL := range []string{
		"https://evil.example/redirect",
		"http://127.0.0.1/internal",
		"https://wiki.biligame.com.evil.example/redirect",
	} {
		parsed, _ := url.Parse(rawURL)
		if err := policy(&http.Request{URL: parsed}, nil); err == nil {
			t.Errorf("不受信任的重定向未被阻断：%s", rawURL)
		}
	}
	allowed, _ := url.Parse("https://wiki.biligame.com/klbq/页面")
	if err := policy(&http.Request{URL: allowed}, nil); err != nil {
		t.Fatalf("同站重定向被错误阻断：%v", err)
	}
}

func TestUntrustedAudioIsSkippedWithWarning(t *testing.T) {
	target, err := ParseURL(samplePageURL)
	if err != nil {
		t.Fatal(err)
	}
	page, err := parseHTMLPage(`<div><audio src="http://127.0.0.1/private.mp3"></audio></div>
		<div><audio src="https://patchwiki.biligame.com/audio/public_CN.mp3"></audio></div>`, target, "")
	if err != nil {
		t.Fatalf("parseHTMLPage() error = %v", err)
	}
	if len(page.Entries) != 1 || len(page.Warnings) != 1 || !strings.Contains(page.Warnings[0], "1 个") {
		t.Fatalf("不受信任音频过滤结果 = entries:%#v warnings:%#v", page.Entries, page.Warnings)
	}
	if page.Entries[0].Audios[0].URL != "https://patchwiki.biligame.com/audio/public_CN.mp3" {
		t.Fatalf("保留的音频地址 = %q", page.Entries[0].Audios[0].URL)
	}
}

func TestTrustedAudioURL(t *testing.T) {
	tests := []struct {
		rawURL  string
		trusted bool
	}{
		{rawURL: "https://patchwiki.biligame.com/audio/voice.mp3", trusted: true},
		{rawURL: "https://i0.hdslb.com/audio/voice.ogg", trusted: true},
		{rawURL: "https://wiki.biligame.com/klbq/Special:Redirect/file/voice.mp3", trusted: true},
		{rawURL: "https://user@patchwiki.biligame.com/audio/voice.mp3", trusted: false},
		{rawURL: "https://patchwiki.biligame.com:8443/audio/voice.mp3", trusted: false},
		{rawURL: "http://127.0.0.1/voice.mp3", trusted: false},
		{rawURL: "https://[::1%25.biligame.com]/voice.mp3", trusted: false},
		{rawURL: "https://biligame.com.evil.example/voice.mp3", trusted: false},
	}
	for _, test := range tests {
		parsed, _ := url.Parse(test.rawURL)
		if got := isTrustedAudioURL(parsed); got != test.trusted {
			t.Errorf("isTrustedAudioURL(%q) = %v, want %v", test.rawURL, got, test.trusted)
		}
	}
}
