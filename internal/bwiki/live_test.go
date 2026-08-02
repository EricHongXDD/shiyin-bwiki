package bwiki

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestLiveSamplePage 用于发布前手动验证用户给出的真实页面，常规测试默认跳过网络访问。
func TestLiveSamplePage(t *testing.T) {
	if os.Getenv("BWIKI_LIVE_TEST") != "1" {
		t.Skip("设置 BWIKI_LIVE_TEST=1 后运行真实页面验证")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	parser := NewParser(Config{
		Timeout: 70 * time.Second,
		UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) " +
			"AppleWebKit/537.36 Chrome/131.0 Safari/537.36 Shiyin/1.0",
	})
	page, err := parser.Parse(ctx, samplePageURL)
	if err != nil {
		t.Fatalf("解析真实示例页失败：%v", err)
	}
	counts := make(map[Language]int)
	variants := 0
	for _, entry := range page.Entries {
		for _, audio := range entry.Audios {
			counts[audio.Language]++
			variants++
		}
	}
	for _, language := range []Language{LanguageZH, LanguageJA, LanguageEN} {
		if counts[language] == 0 {
			t.Fatalf("真实示例页缺少 %s 语音：统计=%v", language, counts)
		}
	}
	t.Logf("页面=%s，台词组=%d，音频=%d，语言统计=%v", page.DisplayTitle, len(page.Entries), variants, counts)

	if os.Getenv("BWIKI_FORMAT_AUDIT") == "1" {
		auditLiveAudioFormats(t, page)
	}
}

// auditLiveAudioFormats 对每种语言抽取一个资源，核对扩展名、响应类型与文件头。
func auditLiveAudioFormats(t *testing.T, page Page) {
	t.Helper()
	samples := make(map[Language]Audio)
	for _, entry := range page.Entries {
		for _, audio := range entry.Audios {
			if _, exists := samples[audio.Language]; !exists {
				samples[audio.Language] = audio
			}
		}
	}

	client := &http.Client{Timeout: 30 * time.Second}
	for _, language := range []Language{LanguageZH, LanguageJA, LanguageEN} {
		audio, exists := samples[language]
		if !exists {
			continue
		}
		request, err := http.NewRequest(http.MethodGet, audio.URL, nil)
		if err != nil {
			t.Fatalf("构造 %s 音频检查请求失败：%v", language, err)
		}
		request.Header.Set("Range", "bytes=0-31")
		request.Header.Set("User-Agent", "Shiyin/1.0 format audit")
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("读取 %s 音频文件头失败：%v", language, err)
		}
		header, readErr := io.ReadAll(io.LimitReader(response.Body, 32))
		response.Body.Close()
		if readErr != nil {
			t.Fatalf("读取 %s 音频文件头失败：%v", language, readErr)
		}
		t.Logf("语言=%s 文件=%s 状态=%s Content-Type=%q Content-Range=%q 文件头=%s 检测格式=%s URL=%s",
			language, audio.FileName, response.Status, response.Header.Get("Content-Type"), response.Header.Get("Content-Range"),
			hex.EncodeToString(header), detectAudioHeader(header), audio.URL)
	}
}

func detectAudioHeader(header []byte) string {
	switch {
	case len(header) >= 12 && bytes.Equal(header[:4], []byte("RIFF")) && bytes.Equal(header[8:12], []byte("WAVE")):
		return "WAV/RIFF"
	case len(header) >= 3 && bytes.Equal(header[:3], []byte("ID3")):
		return "MP3/ID3"
	case len(header) >= 2 && header[0] == 0xff && header[1]&0xe0 == 0xe0:
		return "MP3 帧流或 AAC ADTS"
	case len(header) >= 4 && bytes.Equal(header[:4], []byte("OggS")):
		return "Ogg"
	default:
		return "未知"
	}
}
