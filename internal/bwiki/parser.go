package bwiki

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultTimeout   = 20 * time.Second
	defaultUserAgent = "Shiyin/1.0 (BWIKI voice downloader)"
	maxResponseBytes = 32 << 20
)

// Parser 通过 MediaWiki API 读取页面，并在必要时回退到普通页面 HTML。
type Parser struct {
	client    *http.Client
	userAgent string
}

// NewParser 创建解析器。
func NewParser(config Config) *Parser {
	client := config.Client
	if client == nil {
		client = &http.Client{}
	} else {
		copyClient := *client
		client = &copyClient
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if client.Timeout <= 0 || config.Timeout > 0 {
		client.Timeout = timeout
	}
	client.CheckRedirect = bwikiRedirectPolicy(client.CheckRedirect)
	userAgent := strings.TrimSpace(config.UserAgent)
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	return &Parser{client: client, userAgent: userAgent}
}

// Parse 解析指定 BWIKI 页面。
func (p *Parser) Parse(ctx context.Context, rawURL string) (Page, error) {
	target, err := ParseURL(rawURL)
	if err != nil {
		return Page{}, err
	}

	apiHTML, displayTitle, apiErr := p.fetchAPI(ctx, target)
	var apiParseErr error
	if apiErr == nil {
		page, parseErr := parseHTMLPage(apiHTML, target, displayTitle)
		if parseErr == nil {
			return page, nil
		}
		apiParseErr = parseErr
	}

	directHTML, directErr := p.fetch(ctx, target.SourceURL, "text/html,application/xhtml+xml", "fetchPage")
	if directErr == nil {
		page, parseErr := parseHTMLPage(string(directHTML), target, displayTitle)
		if parseErr == nil {
			return page, nil
		}
		if apiErr != nil && IsKind(apiErr, KindAPI) {
			return Page{}, apiErr
		}
		return Page{}, parseErr
	}
	if apiParseErr != nil {
		return Page{}, apiParseErr
	}
	if apiErr != nil && IsKind(apiErr, KindAPI) {
		return Page{}, apiErr
	}
	return Page{}, directErr
}

func (p *Parser) fetchAPI(ctx context.Context, target Target) (string, string, error) {
	u, err := url.Parse(target.APIURL)
	if err != nil {
		return "", "", parseFailure(KindAPI, "buildAPIRequest", target.APIURL, "无法构造 MediaWiki API 地址", 0, err)
	}
	query := u.Query()
	query.Set("action", "parse")
	query.Set("page", target.PageTitle)
	query.Set("prop", "text|displaytitle")
	query.Set("format", "json")
	query.Set("formatversion", "2")
	query.Set("redirects", "1")
	u.RawQuery = query.Encode()

	body, err := p.fetch(ctx, u.String(), "application/json", "fetchAPI")
	if err != nil {
		return "", "", err
	}
	var response struct {
		Parse struct {
			Title        string          `json:"title"`
			DisplayTitle string          `json:"displaytitle"`
			Text         json.RawMessage `json:"text"`
		} `json:"parse"`
		Error *struct {
			Code string `json:"code"`
			Info string `json:"info"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", "", parseFailure(KindAPI, "decodeAPI", u.String(), "MediaWiki API 返回了无效 JSON", 0, err)
	}
	if response.Error != nil {
		message := strings.TrimSpace(response.Error.Info)
		if message == "" {
			message = response.Error.Code
		}
		return "", "", parseFailure(KindAPI, "parseAPI", u.String(), "MediaWiki API 拒绝解析页面："+message, 0, nil)
	}
	htmlText, err := decodeAPIText(response.Parse.Text)
	if err != nil || strings.TrimSpace(htmlText) == "" {
		return "", "", parseFailure(KindAPI, "parseAPI", u.String(), "MediaWiki API 响应中缺少页面 HTML", 0, err)
	}
	return htmlText, stripHTML(response.Parse.DisplayTitle), nil
}

func decodeAPIText(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var legacy map[string]string
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return "", err
	}
	return legacy["*"], nil
}

func (p *Parser) fetch(ctx context.Context, requestURL, accept, op string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, parseFailure(KindURL, op, requestURL, "无法创建网络请求", 0, err)
	}
	request.Header.Set("User-Agent", p.userAgent)
	request.Header.Set("Accept", accept)
	request.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.6")
	response, err := p.client.Do(request)
	if err != nil {
		return nil, parseFailure(KindNetwork, op, requestURL, "连接 BWIKI 失败："+err.Error(), 0, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := fmt.Sprintf("BWIKI 返回了 HTTP %d（%s）", response.StatusCode, response.Status)
		if response.StatusCode == 567 {
			message = "请求被 BWIKI 的 EdgeOne 安全策略拦截（HTTP 567），请稍后重试或更换网络"
		}
		return nil, parseFailure(KindHTTP, op, requestURL, message, response.StatusCode, nil)
	}
	limited := io.LimitReader(response.Body, maxResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, parseFailure(KindNetwork, op, requestURL, "读取 BWIKI 响应失败："+err.Error(), 0, err)
	}
	if len(body) > maxResponseBytes {
		return nil, parseFailure(KindHTTP, op, requestURL, "BWIKI 页面过大，已停止解析", response.StatusCode, nil)
	}
	return body, nil
}
