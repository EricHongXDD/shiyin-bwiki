package bwiki

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
)

const samplePageURL = "https://wiki.biligame.com/klbq/%E7%B1%B3%E9%9B%AA%E5%84%BF%C2%B7%E6%9D%8E/%E8%AF%AD%E9%9F%B3%E5%8F%B0%E8%AF%8D"

func TestParseURL(t *testing.T) {
	t.Parallel()
	target, err := ParseURL(samplePageURL)
	if err != nil {
		t.Fatalf("ParseURL() error = %v", err)
	}
	if target.WikiBaseURL != "https://wiki.biligame.com/klbq" {
		t.Fatalf("WikiBaseURL = %q", target.WikiBaseURL)
	}
	if target.APIURL != "https://wiki.biligame.com/klbq/api.php" {
		t.Fatalf("APIURL = %q", target.APIURL)
	}
	if target.PageTitle != "米雪儿·李/语音台词" {
		t.Fatalf("PageTitle = %q", target.PageTitle)
	}

	indexTarget, err := ParseURL("https://wiki.biligame.com/klbq/index.php?title=%E7%B1%B3%E9%9B%AA%E5%84%BF_%E8%AF%AD%E9%9F%B3")
	if err != nil {
		t.Fatalf("index.php ParseURL() error = %v", err)
	}
	if indexTarget.PageTitle != "米雪儿 语音" {
		t.Fatalf("index.php PageTitle = %q", indexTarget.PageTitle)
	}
}

func TestParseURLErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		url  string
		kind ErrorKind
	}{
		{name: "不是绝对地址", url: "/klbq/页面", kind: KindURL},
		{name: "错误域名", url: "https://example.com/klbq/页面", kind: KindDomain},
		{name: "非标准端口", url: "https://wiki.biligame.com:8443/klbq/页面", kind: KindURL},
		{name: "缺少标题", url: "https://wiki.biligame.com/klbq", kind: KindURL},
		{name: "错误转义", url: "https://wiki.biligame.com/klbq/%zz", kind: KindURL},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseURL(test.url)
			var parseErr *ParseError
			if !errors.As(err, &parseErr) || parseErr.Kind != test.kind {
				t.Fatalf("ParseURL() error = %#v, want kind %q", err, test.kind)
			}
		})
	}
}

func TestParseHTMLThreeLanguagesAndDuplicateTitles(t *testing.T) {
	t.Parallel()
	markup := readFixture(t, "testdata/three_languages.html")
	target, err := ParseURL(samplePageURL)
	if err != nil {
		t.Fatal(err)
	}
	page, err := parseHTMLPage(markup, target, "<b>米雪儿·李</b>/语音台词")
	if err != nil {
		t.Fatalf("parseHTMLPage() error = %v", err)
	}
	if page.DisplayTitle != "米雪儿·李/语音台词" {
		t.Fatalf("DisplayTitle = %q", page.DisplayTitle)
	}
	if !reflect.DeepEqual(page.Languages, []Language{LanguageZH, LanguageJA, LanguageEN}) {
		t.Fatalf("Languages = %#v", page.Languages)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("len(Entries) = %d, entries = %#v", len(page.Entries), page.Entries)
	}
	first, second := page.Entries[0], page.Entries[1]
	if first.Section != "宿舍" || first.Title != "打招呼" || len(first.Audios) != 3 {
		t.Fatalf("first entry = %#v", first)
	}
	if second.Title != "打招呼" || len(second.Audios) != 1 {
		t.Fatalf("同名标题没有形成独立条目：%#v", second)
	}
	if first.Audios[0].FileName != "Michele_Lounge-0001_CN.mp3" {
		t.Fatalf("FileName = %q", first.Audios[0].FileName)
	}
	if first.Audios[0].URL != "https://patchwiki.biligame.com/audio/8/88/hashed-voice.mp3" {
		t.Fatalf("data-file URL = %q", first.Audios[0].URL)
	}
	if first.Audios[1].URL != "https://patchwiki.biligame.com/audio/Michele_Lounge-0001_JP.mp3" {
		t.Fatalf("audio/src URL = %q", first.Audios[1].URL)
	}
	if first.Audios[2].Text != "Hello, partner!" {
		t.Fatalf("English text = %q", first.Audios[2].Text)
	}
}

func TestParserUsesAPI(t *testing.T) {
	markup := readFixture(t, "testdata/three_languages.html")
	var apiCalls, pageCalls int
	parser, closeServer := testParser(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/klbq/api.php" {
			apiCalls++
			if request.URL.Query().Get("action") != "parse" || request.URL.Query().Get("prop") != "text|displaytitle" {
				t.Errorf("API query = %q", request.URL.RawQuery)
			}
			if request.Header.Get("User-Agent") != "bwiki-test/1.0" {
				t.Errorf("User-Agent = %q", request.Header.Get("User-Agent"))
			}
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"parse": map[string]any{
				"title": "米雪儿·李/语音台词", "displaytitle": "米雪儿·李/语音台词", "text": markup,
			}})
			return
		}
		pageCalls++
		http.Error(writer, "不应请求普通页面", http.StatusInternalServerError)
	})
	defer closeServer()

	page, err := parser.Parse(context.Background(), samplePageURL)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if apiCalls != 1 || pageCalls != 0 || len(page.Entries) != 2 {
		t.Fatalf("apiCalls=%d pageCalls=%d entries=%d", apiCalls, pageCalls, len(page.Entries))
	}
}

func TestParserFallsBackToPageHTML(t *testing.T) {
	markup := readFixture(t, "testdata/three_languages.html")
	parser, closeServer := testParser(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/klbq/api.php" {
			writer.WriteHeader(567)
			return
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = writer.Write([]byte(markup))
	})
	defer closeServer()
	page, err := parser.Parse(context.Background(), samplePageURL)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("len(Entries) = %d", len(page.Entries))
	}
}

func TestParserHTTP567(t *testing.T) {
	parser, closeServer := testParser(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(567)
	})
	defer closeServer()
	_, err := parser.Parse(context.Background(), samplePageURL)
	var parseErr *ParseError
	if !errors.As(err, &parseErr) || parseErr.Kind != KindHTTP || parseErr.StatusCode != 567 {
		t.Fatalf("Parse() error = %#v", err)
	}
	if !strings.Contains(parseErr.Error(), "EdgeOne") {
		t.Fatalf("567 message = %q", parseErr.Error())
	}
}

func TestNoAudioAndChangedStructure(t *testing.T) {
	t.Parallel()
	target, err := ParseURL(samplePageURL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = parseHTMLPage("<html><body><p>这里只有文字</p></body></html>", target, "")
	if !IsKind(err, KindNoAudio) {
		t.Fatalf("no audio error = %#v", err)
	}
	_, err = parseHTMLPage("<html><body><div class=media-audio data-file=''></div></body></html>", target, "")
	if !IsKind(err, KindStructure) {
		t.Fatalf("structure error = %#v", err)
	}
	page, err := parseHTMLPage(`<html><body><div data-file=""><audio src="https://i0.hdslb.com/voice_CN.mp3"></audio></div></body></html>`, target, "")
	if err != nil {
		t.Fatalf("无效 data-file 应回退到 audio/src：%v", err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Audios[0].URL != "https://i0.hdslb.com/voice_CN.mp3" {
		t.Fatalf("audio/src 回退结果 = %#v", page.Entries)
	}
}

func TestLanguagePresentationFallback(t *testing.T) {
	t.Parallel()
	markup := `<h2>宿舍</h2><table><tr><th>生日歌</th>` +
		`<td style="background-color: rgba(255, 192, 203, 0.5)">` +
		`<div class="media-audio" data-file="https://patchwiki.biligame.com/audio/song.mp3"></div></td>` +
		`<td></td></tr></table>`
	target, err := ParseURL(samplePageURL)
	if err != nil {
		t.Fatal(err)
	}
	page, err := parseHTMLPage(markup, target, "")
	if err != nil {
		t.Fatalf("parseHTMLPage() error = %v", err)
	}
	if got := page.Entries[0].Audios[0].Language; got != LanguageZH {
		t.Fatalf("背景色语言兜底 = %q，期望 %q", got, LanguageZH)
	}
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func testParser(t *testing.T, handler http.HandlerFunc) (*Parser, func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	client := server.Client()
	transport := client.Transport
	client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		clone := request.Clone(request.Context())
		clone.URL.Scheme = serverURL.Scheme
		clone.URL.Host = serverURL.Host
		clone.Host = ""
		return transport.RoundTrip(clone)
	})
	return NewParser(Config{Client: client, UserAgent: "bwiki-test/1.0"}), server.Close
}
