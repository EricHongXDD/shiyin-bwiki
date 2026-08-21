package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/EricHongXDD/shiyin-bwiki/internal/bwiki"
	"github.com/EricHongXDD/shiyin-bwiki/internal/download"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

var appVersion = "1.3.0-dev"

const (
	defaultDownloadConcurrency = 4
	minDownloadConcurrency     = 1
	maxDownloadConcurrency     = 32
	githubLatestReleaseURL     = "https://api.github.com/repos/EricHongXDD/shiyin-bwiki/releases/latest"
)

// AppSettings 保存桌面应用的用户设置。
type AppSettings struct {
	DownloadConcurrency int  `json:"downloadConcurrency"`
	AutoCheckUpdates    bool `json:"autoCheckUpdates"`
}

// UpdateInfo 是 GitHub Release 更新检查结果。
type UpdateInfo struct {
	CurrentVersion  string `json:"currentVersion"`
	LatestVersion   string `json:"latestVersion"`
	UpdateAvailable bool   `json:"updateAvailable"`
	ReleaseURL      string `json:"releaseUrl"`
	DownloadURL     string `json:"downloadUrl"`
	PublishedAt     string `json:"publishedAt,omitempty"`
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type githubReleasePayload struct {
	TagName     string               `json:"tag_name"`
	HTMLURL     string               `json:"html_url"`
	PublishedAt string               `json:"published_at"`
	Assets      []githubReleaseAsset `json:"assets"`
}

// App 是前端可调用的应用服务边界。
type App struct {
	ctx          context.Context
	parser       *bwiki.Parser
	downloads    *download.Manager
	downloadDir  string
	settingsPath string
	settings     AppSettings
	contextMu    sync.RWMutex
	directoryMu  sync.RWMutex
	settingsMu   sync.RWMutex
}

// Bootstrap 是界面启动时需要的一次性状态。
type Bootstrap struct {
	DownloadDirectory string          `json:"downloadDirectory"`
	Tasks             []download.Task `json:"tasks"`
	Version           string          `json:"version"`
	Settings          AppSettings     `json:"settings"`
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
	RoleName  string        `json:"roleName"`
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
	RoleName  string              `json:"roleName"`
	Items     []QueueDownloadItem `json:"items"`
}

type QueueDownloadItem struct {
	Type         string `json:"type,omitempty"`
	SourceID     string `json:"sourceId"`
	Title        string `json:"title"`
	Category     string `json:"category"`
	Language     string `json:"language"`
	LanguageName string `json:"languageName"`
	FileName     string `json:"fileName"`
	URL          string `json:"url"`
	Content      string `json:"content,omitempty"`
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
	settingsPath := filepath.Join(stateDir, "settings.json")
	settings, err := loadAppSettings(settingsPath)
	if err != nil {
		return nil, fmt.Errorf("读取应用设置失败：%w", err)
	}
	app := &App{
		downloadDir:  downloadDir,
		settingsPath: settingsPath,
		settings:     settings,
		parser: bwiki.NewParser(bwiki.Config{
			Client: &http.Client{Timeout: 50 * time.Second},
			UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) " +
				"AppleWebKit/537.36 Chrome/131.0 Safari/537.36 Shiyin/" + appVersion,
			Timeout: 50 * time.Second,
		}),
	}

	manager, err := download.NewManager(download.Config{
		StatePath:   filepath.Join(stateDir, "tasks.json"),
		Concurrency: settings.DownloadConcurrency,
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
		Settings:          a.currentSettings(),
	}
}

func defaultAppSettings() AppSettings {
	return AppSettings{
		DownloadConcurrency: defaultDownloadConcurrency,
		AutoCheckUpdates:    true,
	}
}

func loadAppSettings(path string) (AppSettings, error) {
	settings := defaultAppSettings()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	var stored struct {
		DownloadConcurrency *int  `json:"downloadConcurrency"`
		AutoCheckUpdates    *bool `json:"autoCheckUpdates"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		return settings, fmt.Errorf("解析设置文件：%w", err)
	}
	if stored.DownloadConcurrency != nil && *stored.DownloadConcurrency >= minDownloadConcurrency && *stored.DownloadConcurrency <= maxDownloadConcurrency {
		settings.DownloadConcurrency = *stored.DownloadConcurrency
	}
	if stored.AutoCheckUpdates != nil {
		settings.AutoCheckUpdates = *stored.AutoCheckUpdates
	}
	return settings, nil
}

func saveAppSettings(path string, settings AppSettings) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("编码应用设置：%w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func normalizeAppSettings(settings AppSettings) (AppSettings, error) {
	if settings.DownloadConcurrency < minDownloadConcurrency || settings.DownloadConcurrency > maxDownloadConcurrency {
		return AppSettings{}, fmt.Errorf("下载并发数必须在 %d-%d 之间", minDownloadConcurrency, maxDownloadConcurrency)
	}
	return settings, nil
}

func (a *App) currentSettings() AppSettings {
	a.settingsMu.RLock()
	defer a.settingsMu.RUnlock()
	return a.settings
}

// SaveSettings 保存用户设置，并立即调整下载 worker 数量。
func (a *App) SaveSettings(request AppSettings) (AppSettings, error) {
	settings, err := normalizeAppSettings(request)
	if err != nil {
		return AppSettings{}, err
	}
	previous := a.currentSettings()
	if a.settingsPath != "" {
		if err := saveAppSettings(a.settingsPath, settings); err != nil {
			return AppSettings{}, fmt.Errorf("保存应用设置：%w", err)
		}
	}
	if a.downloads != nil && settings.DownloadConcurrency != previous.DownloadConcurrency {
		if err := a.downloads.SetConcurrency(settings.DownloadConcurrency); err != nil {
			if a.settingsPath != "" {
				_ = saveAppSettings(a.settingsPath, previous)
			}
			return AppSettings{}, err
		}
	}
	a.settingsMu.Lock()
	a.settings = settings
	a.settingsMu.Unlock()
	return settings, nil
}

// CheckForUpdates 查询 GitHub 最新正式 Release，并返回当前平台可下载地址。
func (a *App) CheckForUpdates() (UpdateInfo, error) {
	ctx, cancel := context.WithTimeout(a.appContext(), 12*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, githubLatestReleaseURL, nil)
	if err != nil {
		return UpdateInfo{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "Shiyin/"+appVersion)
	response, err := (&http.Client{Timeout: 12 * time.Second}).Do(request)
	if err != nil {
		return UpdateInfo{}, fmt.Errorf("连接 GitHub 更新服务失败：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return UpdateInfo{}, fmt.Errorf("GitHub 更新服务返回 HTTP %d", response.StatusCode)
	}
	var release githubReleasePayload
	if err := json.NewDecoder(response.Body).Decode(&release); err != nil {
		return UpdateInfo{}, fmt.Errorf("解析 GitHub Release 失败：%w", err)
	}
	latest := normalizeReleaseVersion(release.TagName)
	if latest == "" {
		return UpdateInfo{}, errors.New("GitHub Release 没有有效版本号")
	}
	releaseURL := strings.TrimSpace(release.HTMLURL)
	if releaseURL == "" {
		releaseURL = "https://github.com/EricHongXDD/shiyin-bwiki/releases/latest"
	}
	info := UpdateInfo{
		CurrentVersion:  normalizeReleaseVersion(appVersion),
		LatestVersion:   latest,
		UpdateAvailable: compareReleaseVersions(latest, appVersion) > 0,
		ReleaseURL:      releaseURL,
		DownloadURL:     releaseURL,
		PublishedAt:     release.PublishedAt,
	}
	for _, asset := range release.Assets {
		if strings.HasSuffix(strings.ToLower(asset.Name), "-windows-amd64-setup.exe") && strings.TrimSpace(asset.BrowserDownloadURL) != "" {
			info.DownloadURL = asset.BrowserDownloadURL
			break
		}
	}
	return info, nil
}

func normalizeReleaseVersion(raw string) string {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(raw, "v"), "V"))
	if index := strings.IndexAny(raw, "-+"); index >= 0 {
		raw = raw[:index]
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return ""
	}
	values := make([]int, 3)
	for index, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return ""
		}
		values[index] = value
	}
	return fmt.Sprintf("%d.%d.%d", values[0], values[1], values[2])
}

func compareReleaseVersions(left, right string) int {
	left = normalizeReleaseVersion(left)
	right = normalizeReleaseVersion(right)
	if left == "" || right == "" {
		return 0
	}
	leftParts := strings.Split(left, ".")
	rightParts := strings.Split(right, ".")
	for index := range leftParts {
		leftValue, _ := strconv.Atoi(leftParts[index])
		rightValue, _ := strconv.Atoi(rightParts[index])
		if leftValue > rightValue {
			return 1
		}
		if leftValue < rightValue {
			return -1
		}
	}
	return 0
}

// OpenExternalURL 只允许打开 GitHub HTTPS 页面或 Release 资产。
func (a *App) OpenExternalURL(rawURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") {
		return errors.New("只允许打开 GitHub HTTPS 链接")
	}
	wailsruntime.BrowserOpenURL(a.appContext(), parsed.String())
	return nil
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
	if len(request.Items) > 10000 {
		return nil, errors.New("一次最多添加 10000 条下载任务")
	}

	roleName := strings.TrimSpace(request.RoleName)
	items := make([]download.NewTask, 0, len(request.Items))
	for _, item := range request.Items {
		taskType := strings.ToLower(strings.TrimSpace(item.Type))
		if taskType == "" {
			taskType = download.TaskTypeAudio
		}
		if taskType == download.TaskTypeAudio {
			u, err := url.Parse(item.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
				return nil, fmt.Errorf("音频“%s”的下载地址无效", item.FileName)
			}
		} else if taskType != download.TaskTypeText {
			return nil, fmt.Errorf("文件“%s”的任务类型无效", item.FileName)
		}
		items = append(items, download.NewTask{
			Type:         taskType,
			SourceID:     item.SourceID,
			Title:        item.Title,
			Category:     item.Category,
			Language:     item.Language,
			LanguageName: item.LanguageName,
			FileName:     item.FileName,
			URL:          item.URL,
			Directory:    directory,
			Subdirectory: roleName,
			Content:      item.Content,
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

func (a *App) DownloadTaskSubtitle(id string) (download.Task, error) {
	return a.downloads.AddSubtitleTask(id)
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
		RoleName:  roleNameFromTitle(title),
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

func roleNameFromTitle(title string) string {
	title = strings.TrimSpace(title)
	if index := strings.IndexAny(title, "/／"); index >= 0 {
		if roleName := strings.TrimSpace(title[:index]); roleName != "" {
			return roleName
		}
	}
	for _, suffix := range []string{"语音台词页", "语音台词", "语音"} {
		if strings.HasSuffix(title, suffix) {
			if roleName := strings.TrimSpace(strings.TrimSuffix(title, suffix)); roleName != "" {
				return roleName
			}
		}
	}
	return title
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
