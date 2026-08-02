package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/EricHongXDD/shiyin-bwiki/internal/bwiki"
	"github.com/EricHongXDD/shiyin-bwiki/internal/download"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

var appVersion = "1.0.0-dev"

// App 是前端可调用的应用服务边界。
type App struct {
	ctx         context.Context
	parser      *bwiki.Parser
	downloads   *download.Manager
	downloadDir string
	contextMu   sync.RWMutex
	directoryMu sync.RWMutex
}

// Bootstrap 是界面启动时需要的一次性状态。
type Bootstrap struct {
	DownloadDirectory string          `json:"downloadDirectory"`
	Tasks             []download.Task `json:"tasks"`
	Version           string          `json:"version"`
}

// ParseResponse 避免把可诊断错误压缩成一段普通字符串。
type ParseResponse struct {
	OK    bool      `json:"ok"`
	Page  *PageDTO  `json:"page,omitempty"`
	Error *ErrorDTO `json:"error,omitempty"`
}

// ErrorDTO 是呈现给用户的结构化失败原因。
type ErrorDTO struct {
	Code       string `json:"code"`
	Stage      string `json:"stage"`
	Message    string `json:"message"`
	Detail     string `json:"detail,omitempty"`
	Retryable  bool   `json:"retryable"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
}

// PageDTO 是面向界面的稳定数据模型，与页面 DOM 细节解耦。
type PageDTO struct {
	Title     string        `json:"title"`
	SourceURL string        `json:"sourceUrl"`
	Entries   []EntryDTO    `json:"entries"`
	Languages []LanguageDTO `json:"languages"`
	Warnings  []string      `json:"warnings"`
}

type EntryDTO struct {
	ID       string       `json:"id"`
	Category string       `json:"category"`
	Title    string       `json:"title"`
	Variants []VariantDTO `json:"variants"`
}

type VariantDTO struct {
	ID           string `json:"id"`
	Language     string `json:"language"`
	LanguageName string `json:"languageName"`
	Transcript   string `json:"transcript"`
	FileName     string `json:"fileName"`
	URL          string `json:"url"`
}

type LanguageDTO struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type QueueRequest struct {
	Directory string              `json:"directory"`
	Items     []QueueDownloadItem `json:"items"`
}

type QueueDownloadItem struct {
	SourceID     string `json:"sourceId"`
	Title        string `json:"title"`
	Category     string `json:"category"`
	Language     string `json:"language"`
	LanguageName string `json:"languageName"`
	FileName     string `json:"fileName"`
	URL          string `json:"url"`
}

func NewApp() (*App, error) {
	configRoot, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("获取配置目录失败：%w", err)
	}
	stateDir := applicationStateDirectory(configRoot)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建配置目录失败：%w", err)
	}

	downloadDir := defaultDownloadDirectory()
	app := &App{
		downloadDir: downloadDir,
		parser: bwiki.NewParser(bwiki.Config{
			Client: &http.Client{Timeout: 50 * time.Second},
			UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) " +
				"AppleWebKit/537.36 Chrome/131.0 Safari/537.36 Shiyin/" + appVersion,
			Timeout: 50 * time.Second,
		}),
	}

	manager, err := download.NewManager(download.Config{
		StatePath:   filepath.Join(stateDir, "tasks.json"),
		Concurrency: 4,
		Client:      &http.Client{Timeout: 0},
		OnUpdate:    app.onTaskUpdate,
	})
	if err != nil {
		return nil, fmt.Errorf("初始化下载任务失败：%w", err)
	}
	app.downloads = manager
	return app, nil
}

func (a *App) startup(ctx context.Context) {
	a.contextMu.Lock()
	a.ctx = ctx
	a.contextMu.Unlock()
}

func (a *App) shutdown(context.Context) {
	if a.downloads != nil {
		if err := a.downloads.Close(); err != nil {
			log.Printf("保存下载任务状态失败：%v", err)
		}
	}
}

func (a *App) GetBootstrap() Bootstrap {
	a.directoryMu.RLock()
	directory := a.downloadDir
	a.directoryMu.RUnlock()
	return Bootstrap{
		DownloadDirectory: directory,
		Tasks:             a.downloads.List(),
		Version:           appVersion,
	}
}

func (a *App) ParsePage(rawURL string) ParseResponse {
	ctx, cancel := context.WithTimeout(a.appContext(), 55*time.Second)
	defer cancel()

	page, err := a.parser.Parse(ctx, strings.TrimSpace(rawURL))
	if err != nil {
		return ParseResponse{OK: false, Error: mapParseError(err)}
	}
	dto := mapPage(page)
	return ParseResponse{OK: true, Page: &dto}
}

func (a *App) ChooseDownloadDirectory() (string, error) {
	a.directoryMu.RLock()
	defaultDir := a.downloadDir
	a.directoryMu.RUnlock()
	if err := os.MkdirAll(defaultDir, 0o755); err != nil {
		return "", fmt.Errorf("创建默认下载目录失败：%w", err)
	}
	directory, err := wailsruntime.OpenDirectoryDialog(a.appContext(), wailsruntime.OpenDialogOptions{
		Title:            "选择语音保存位置",
		DefaultDirectory: defaultDir,
	})
	if err != nil || directory == "" {
		return directory, err
	}
	cleanDirectory := filepath.Clean(directory)
	a.directoryMu.Lock()
	a.downloadDir = cleanDirectory
	a.directoryMu.Unlock()
	return cleanDirectory, nil
}

func (a *App) QueueDownloads(request QueueRequest) ([]download.Task, error) {
	directory := strings.TrimSpace(request.Directory)
	if directory == "" {
		a.directoryMu.RLock()
		directory = a.downloadDir
		a.directoryMu.RUnlock()
	}
	directory = filepath.Clean(directory)
	if !filepath.IsAbs(directory) {
		return nil, errors.New("下载目录必须是绝对路径")
	}
	if len(request.Items) == 0 {
		return nil, errors.New("请至少选择一条语音")
	}
	if len(request.Items) > 5000 {
		return nil, errors.New("一次最多添加 5000 条下载任务")
	}

	items := make([]download.NewTask, 0, len(request.Items))
	for _, item := range request.Items {
		u, err := url.Parse(item.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return nil, fmt.Errorf("音频“%s”的下载地址无效", item.FileName)
		}
		items = append(items, download.NewTask{
			SourceID:     item.SourceID,
			Title:        item.Title,
			Category:     item.Category,
			Language:     item.Language,
			LanguageName: item.LanguageName,
			FileName:     item.FileName,
			URL:          item.URL,
			Directory:    directory,
		})
	}
	a.directoryMu.Lock()
	a.downloadDir = directory
	a.directoryMu.Unlock()
	return a.downloads.AddBatch(items)
}

func (a *App) PauseTask(id string) error {
	return a.downloads.Pause(id)
}

func (a *App) ResumeTask(id string) error {
	return a.downloads.Resume(id)
}

func (a *App) RetryTask(id string) error {
	return a.downloads.Retry(id)
}

func (a *App) CancelTask(id string) error {
	return a.downloads.Cancel(id)
}

func (a *App) RemoveTask(id string) error {
	return a.downloads.Remove(id)
}

func (a *App) ClearCompletedTasks() error {
	_, err := a.downloads.ClearCompleted()
	return err
}

func (a *App) OpenTaskFolder(id string) error {
	task, ok := a.downloads.Lookup(id)
	if !ok {
		return errors.New("下载任务不存在")
	}
	directory := filepath.Dir(task.OutputPath)
	if runtime.GOOS == "windows" {
		return exec.Command("explorer.exe", directory).Start()
	}
	folderURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(directory)}).String()
	wailsruntime.BrowserOpenURL(a.appContext(), folderURL)
	return nil
}

func (a *App) appContext() context.Context {
	a.contextMu.RLock()
	defer a.contextMu.RUnlock()
	if a.ctx == nil {
		return context.Background()
	}
	return a.ctx
}

func (a *App) onTaskUpdate(task download.Task) {
	a.contextMu.RLock()
	ctx := a.ctx
	a.contextMu.RUnlock()
	if ctx != nil {
		wailsruntime.EventsEmit(ctx, "download:task", task)
	}
}

func defaultDownloadDirectory() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "Shiyin")
	}
	return filepath.Join(home, "Downloads", "Shiyin")
}

func applicationStateDirectory(configRoot string) string {
	preferred := filepath.Join(configRoot, "Shiyin")
	if info, err := os.Stat(preferred); err == nil && info.IsDir() {
		return preferred
	}

	// 旧版本使用 BWIKIAudio 目录；继续读取它可保留历史任务和断点数据。
	legacy := filepath.Join(configRoot, "BWIKIAudio")
	if info, err := os.Stat(legacy); err == nil && info.IsDir() {
		return legacy
	}
	return preferred
}

func mapPage(page bwiki.Page) PageDTO {
	title := strings.TrimSpace(page.DisplayTitle)
	if title == "" {
		title = page.PageTitle
	}
	dto := PageDTO{
		Title:     title,
		SourceURL: page.SourceURL,
		Entries:   make([]EntryDTO, 0, len(page.Entries)),
		Languages: make([]LanguageDTO, 0, 4),
		Warnings:  append([]string(nil), page.Warnings...),
	}

	languageCounts := make(map[string]*LanguageDTO)
	for entryIndex, entry := range page.Entries {
		entryID := stableID(page.SourceURL, fmt.Sprintf("%d", entryIndex), entry.Section, entry.Title)
		entryDTO := EntryDTO{
			ID:       entryID,
			Category: fallback(entry.Section, "未分组"),
			Title:    fallback(entry.Title, "未命名语音"),
			Variants: make([]VariantDTO, 0, len(entry.Audios)),
		}
		for _, audio := range entry.Audios {
			code := fallback(string(audio.Language), "unknown")
			name := languageDisplayName(code)
			variant := VariantDTO{
				ID:           stableID(audio.URL, code),
				Language:     code,
				LanguageName: name,
				Transcript:   strings.TrimSpace(audio.Text),
				FileName:     audio.FileName,
				URL:          audio.URL,
			}
			entryDTO.Variants = append(entryDTO.Variants, variant)
			language := languageCounts[code]
			if language == nil {
				language = &LanguageDTO{Code: code, Name: name}
				languageCounts[code] = language
			}
			language.Count++
		}
		if len(entryDTO.Variants) > 0 {
			dto.Entries = append(dto.Entries, entryDTO)
		}
	}

	for _, language := range languageCounts {
		dto.Languages = append(dto.Languages, *language)
	}
	sort.Slice(dto.Languages, func(i, j int) bool {
		left := languagePriority(dto.Languages[i].Code)
		right := languagePriority(dto.Languages[j].Code)
		if left == right {
			return dto.Languages[i].Name < dto.Languages[j].Name
		}
		return left < right
	})
	return dto
}

func mapParseError(err error) *ErrorDTO {
	result := &ErrorDTO{
		Code:      "unknown",
		Stage:     "parse",
		Message:   "解析页面失败",
		Detail:    err.Error(),
		Retryable: false,
	}
	var parseErr *bwiki.ParseError
	if !errors.As(err, &parseErr) {
		return result
	}
	switch parseErr.Kind {
	case bwiki.KindURL:
		result.Code = "invalid_url"
	case bwiki.KindDomain:
		result.Code = "unsupported_host"
	case bwiki.KindNetwork:
		result.Code = "network_error"
		if errors.Is(parseErr.Err, context.DeadlineExceeded) {
			result.Code = "timeout"
		}
	case bwiki.KindHTTP:
		result.Code = "http_error"
		switch parseErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			result.Code = "access_denied"
		case http.StatusNotFound:
			result.Code = "page_not_found"
		case http.StatusTooManyRequests:
			result.Code = "rate_limited"
		}
	case bwiki.KindAPI:
		result.Code = "parse_failed"
	case bwiki.KindNoAudio:
		result.Code = "no_audio"
	case bwiki.KindStructure:
		result.Code = "structure_changed"
	default:
		result.Code = string(parseErr.Kind)
	}
	result.Stage = parseErr.Op
	result.Message = fallback(parseErr.Message, result.Message)
	result.HTTPStatus = parseErr.StatusCode
	result.Retryable = parseErr.Kind == bwiki.KindNetwork ||
		(parseErr.Kind == bwiki.KindHTTP && (parseErr.StatusCode == 429 || parseErr.StatusCode >= 500))
	if parseErr.Err != nil {
		result.Detail = parseErr.Err.Error()
	}
	if parseErr.StatusCode == 567 {
		result.Message = "请求被 BWIKI 的安全策略拦截"
		result.Detail = "站点返回 HTTP 567。请稍后重试，或更换网络后再次解析。"
		result.Retryable = true
	}
	return result
}

func stableID(parts ...string) string {
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(hash[:10])
}

func fallback(value, fallbackValue string) string {
	if strings.TrimSpace(value) == "" {
		return fallbackValue
	}
	return strings.TrimSpace(value)
}

func languageDisplayName(code string) string {
	switch strings.ToLower(code) {
	case "zh", "cn", "zh-cn":
		return "中文"
	case "ja", "jp":
		return "日语"
	case "en":
		return "英语"
	default:
		return "未识别"
	}
}

func languagePriority(code string) int {
	switch strings.ToLower(code) {
	case "zh", "cn", "zh-cn":
		return 0
	case "ja", "jp":
		return 1
	case "en":
		return 2
	default:
		return 10
	}
}
