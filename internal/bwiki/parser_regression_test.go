package bwiki

import "testing"

func TestAdjacentTranscriptLanguageAttributes(t *testing.T) {
	t.Parallel()
	page := parseRegressionPage(t, `<table>
		<tr><th rowspan="3">无后缀语言</th><td><audio src="https://i0.hdslb.com/clip-1.mp3"></audio></td><td lang="zh-CN">OK</td></tr>
		<tr><td><audio src="https://i0.hdslb.com/clip-2.mp3"></audio></td><td lang="ja">勝利</td></tr>
		<tr><td><audio src="https://i0.hdslb.com/clip-3.mp3"></audio></td><td lang="en">123</td></tr>
	</table>`)

	if len(page.Entries) != 1 || len(page.Entries[0].Audios) != 3 {
		t.Fatalf("相邻 lang 单元格解析结果 = %#v", page.Entries)
	}
	want := []Language{LanguageZH, LanguageJA, LanguageEN}
	for index, audio := range page.Entries[0].Audios {
		if audio.Language != want[index] {
			t.Fatalf("第 %d 条语言 = %q，期望 %q", index+1, audio.Language, want[index])
		}
	}
}

func TestSameAudioURLInDifferentRowsIsPreserved(t *testing.T) {
	t.Parallel()
	const sharedURL = "https://i0.hdslb.com/shared.mp3"
	page := parseRegressionPage(t, `<table>
		<tr><th>第一句</th><td><div class="media-audio" data-file="`+sharedURL+`"></div><audio src="`+sharedURL+`"></audio></td><td>第一句台词</td></tr>
		<tr><th>第二句</th><td><audio src="`+sharedURL+`"></audio></td><td>第二句台词</td></tr>
	</table>`)

	if len(page.Entries) != 2 {
		t.Fatalf("复用同一 URL 的独立条目数 = %d，结果 = %#v", len(page.Entries), page.Entries)
	}
	for index, entry := range page.Entries {
		if len(entry.Audios) != 1 || entry.Audios[0].URL != sharedURL {
			t.Fatalf("第 %d 个条目未正确按单元格去重：%#v", index+1, entry)
		}
	}
}

func TestRowspanTitleSurvivesInvalidFirstAudio(t *testing.T) {
	t.Parallel()
	page := parseRegressionPage(t, `<table>
		<tr><th rowspan="3">首行音频失效</th><td><audio src=""></audio></td><td lang="zh-CN">缺失</td></tr>
		<tr><td><audio src="https://i0.hdslb.com/clip-ja.mp3"></audio></td><td lang="ja">こんにちは</td></tr>
		<tr><td><audio src="https://i0.hdslb.com/clip-en.mp3"></audio></td><td lang="en">Hello</td></tr>
	</table>`)

	if len(page.Entries) != 1 {
		t.Fatalf("rowspan 首行失效后条目数 = %d，结果 = %#v", len(page.Entries), page.Entries)
	}
	entry := page.Entries[0]
	if entry.Title != "首行音频失效" || len(entry.Audios) != 2 {
		t.Fatalf("rowspan 首行失效后的分组 = %#v", entry)
	}
	if entry.Audios[0].Language != LanguageJA || entry.Audios[1].Language != LanguageEN {
		t.Fatalf("rowspan 后续语言 = %#v", entry.Audios)
	}
}

func parseRegressionPage(t *testing.T, markup string) Page {
	t.Helper()
	target, err := ParseURL(samplePageURL)
	if err != nil {
		t.Fatal(err)
	}
	page, err := parseHTMLPage(markup, target, "")
	if err != nil {
		t.Fatalf("parseHTMLPage() error = %v", err)
	}
	return page
}
