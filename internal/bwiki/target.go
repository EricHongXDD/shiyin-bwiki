package bwiki

import (
	"fmt"
	"net/url"
	"strings"
)

// Target 是从用户输入 URL 中得到的 MediaWiki 请求信息。
type Target struct {
	SourceURL   string `json:"sourceURL"`
	WikiBaseURL string `json:"wikiBaseURL"`
	APIURL      string `json:"apiURL"`
	PageTitle   string `json:"pageTitle"`
}

// ParseURL 校验 BWIKI 页面 URL，并解析 wiki 基路径和页面标题。
func ParseURL(rawURL string) (Target, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return Target{}, parseFailure(KindURL, "parseURL", rawURL, "请输入 BWIKI 页面 URL", 0, nil)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return Target{}, parseFailure(KindURL, "parseURL", rawURL, "URL 格式不正确："+err.Error(), 0, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || u.User != nil {
		return Target{}, parseFailure(KindURL, "parseURL", rawURL, "URL 必须是完整的 HTTP 或 HTTPS 页面地址", 0, nil)
	}
	hostname := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if hostname != "wiki.biligame.com" {
		return Target{}, parseFailure(KindDomain, "parseURL", rawURL, "仅支持 wiki.biligame.com 域名下的 BWIKI 页面", 0, nil)
	}
	if !isSafeWebURL(u) {
		return Target{}, parseFailure(KindURL, "parseURL", rawURL, "BWIKI 地址只能使用标准 HTTP 或 HTTPS 端口", 0, nil)
	}

	parts := splitPath(u.Path)
	if len(parts) < 2 {
		return Target{}, parseFailure(KindURL, "parseURL", rawURL, "URL 中缺少 wiki 名称或页面标题", 0, nil)
	}
	wikiName := parts[0]
	if wikiName == "." || wikiName == ".." || strings.EqualFold(wikiName, "api.php") {
		return Target{}, parseFailure(KindURL, "parseURL", rawURL, "无法识别 URL 中的 wiki 基路径", 0, nil)
	}

	var title string
	if strings.EqualFold(parts[1], "index.php") {
		title = u.Query().Get("title")
	} else {
		title = strings.Join(parts[1:], "/")
	}
	title = normalizeTitle(title)
	if title == "" {
		return Target{}, parseFailure(KindURL, "parseURL", rawURL, "URL 中缺少 MediaWiki 页面标题", 0, nil)
	}

	base := &url.URL{Scheme: strings.ToLower(u.Scheme), Host: u.Host, Path: "/" + wikiName}
	baseURL := strings.TrimSuffix(base.String(), "/")
	u.Fragment = ""
	return Target{
		SourceURL:   u.String(),
		WikiBaseURL: baseURL,
		APIURL:      baseURL + "/api.php",
		PageTitle:   title,
	}, nil
}

func splitPath(value string) []string {
	value = strings.Trim(value, "/")
	if value == "" {
		return nil
	}
	parts := strings.Split(value, "/")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func normalizeTitle(value string) string {
	value = strings.ReplaceAll(value, "_", " ")
	segments := strings.Split(value, "/")
	for i := range segments {
		segments[i] = cleanTextValue(segments[i])
	}
	return strings.Trim(strings.Join(segments, "/"), "/")
}

func specialFileURL(baseURL, fileName string) string {
	fileName = strings.TrimSpace(fileName)
	segments := strings.Split(fileName, "/")
	for i := range segments {
		segments[i] = url.PathEscape(segments[i])
	}
	return baseURL + "/Special:Redirect/file/" + strings.Join(segments, "/")
}

func parseFailure(kind ErrorKind, op, requestURL, message string, status int, err error) error {
	if message == "" && err != nil {
		message = err.Error()
	}
	return &ParseError{Kind: kind, Op: op, URL: requestURL, Message: message, StatusCode: status, Err: err}
}

func invalidAudioReference(raw string) error {
	return fmt.Errorf("无法识别音频地址 %q", raw)
}
