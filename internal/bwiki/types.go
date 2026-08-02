package bwiki

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Language 是页面中语音使用的语言代码。
type Language string

const (
	LanguageZH      Language = "zh"
	LanguageJA      Language = "ja"
	LanguageEN      Language = "en"
	LanguageUnknown Language = "unknown"
)

// Audio 描述同一句台词的一种语言版本。
type Audio struct {
	Language Language `json:"language"`
	Text     string   `json:"text"`
	FileName string   `json:"fileName"`
	URL      string   `json:"url"`
}

// Entry 是一条台词；Audios 中可同时包含中、日、英等多个版本。
type Entry struct {
	Section string  `json:"section"`
	Title   string  `json:"title"`
	Audios  []Audio `json:"audios"`
}

// Page 是一个 BWIKI 页面解析后的结果。
type Page struct {
	SourceURL    string     `json:"sourceURL"`
	WikiBaseURL  string     `json:"wikiBaseURL"`
	PageTitle    string     `json:"pageTitle"`
	DisplayTitle string     `json:"displayTitle"`
	Languages    []Language `json:"languages"`
	Entries      []Entry    `json:"entries"`
	Warnings     []string   `json:"warnings,omitempty"`
}

// Config 配置网络客户端。Client 为空时会创建默认客户端。
type Config struct {
	Client    *http.Client
	UserAgent string
	Timeout   time.Duration
}

// ErrorKind 是可供界面稳定判断的解析失败类别。
type ErrorKind string

const (
	KindURL       ErrorKind = "url"
	KindDomain    ErrorKind = "domain"
	KindNetwork   ErrorKind = "network"
	KindHTTP      ErrorKind = "http"
	KindAPI       ErrorKind = "api"
	KindNoAudio   ErrorKind = "noAudio"
	KindStructure ErrorKind = "structure"
)

// ParseError 保留面向用户的失败原因以及底层错误。
type ParseError struct {
	Kind       ErrorKind `json:"kind"`
	Op         string    `json:"op,omitempty"`
	URL        string    `json:"url,omitempty"`
	Message    string    `json:"message"`
	StatusCode int       `json:"statusCode,omitempty"`
	Err        error     `json:"-"`
}

func (e *ParseError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("BWIKI 解析失败（%s）", e.Kind)
}

func (e *ParseError) Unwrap() error { return e.Err }

// IsKind 判断错误链中是否存在指定的解析错误类别。
func IsKind(err error, kind ErrorKind) bool {
	var parseErr *ParseError
	return errors.As(err, &parseErr) && parseErr.Kind == kind
}
