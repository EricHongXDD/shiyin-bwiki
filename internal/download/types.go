package download

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Status 表示下载任务当前所处的生命周期状态。
type Status string

const (
	StatusQueued      Status = "queued"
	StatusDownloading Status = "downloading"
	StatusPaused      Status = "paused"
	StatusCompleted   Status = "completed"
	StatusFailed      Status = "failed"
	StatusCanceled    Status = "canceled"
)

var (
	ErrClosed            = errors.New("下载管理器已关闭")
	ErrTaskNotFound      = errors.New("下载任务不存在")
	ErrInvalidTransition = errors.New("当前任务状态不允许此操作")
)

// Config 配置下载管理器。Concurrency 小于等于零时默认使用 4。
type Config struct {
	StatePath        string
	Concurrency      int
	Client           *http.Client
	OnUpdate         func(Task)
	ProgressInterval time.Duration
	// AllowPrivateNetwork 只用于显式信任本机测试服务器的场景；桌面应用保持 false。
	AllowPrivateNetwork bool
}

// NewTask 是创建下载任务时需要的输入。
type NewTask struct {
	SourceID     string `json:"sourceId"`
	Title        string `json:"title"`
	Category     string `json:"category"`
	Language     string `json:"language"`
	LanguageName string `json:"languageName"`
	FileName     string `json:"fileName"`
	URL          string `json:"url"`
	Directory    string `json:"directory"`
}

// Task 是可直接用于界面展示和 JSON 持久化的下载任务 DTO。
type Task struct {
	ID           string     `json:"id"`
	BatchID      string     `json:"batchId,omitempty"`
	SourceID     string     `json:"sourceId"`
	Title        string     `json:"title"`
	Category     string     `json:"category"`
	Language     string     `json:"language"`
	LanguageName string     `json:"languageName"`
	FileName     string     `json:"fileName"`
	URL          string     `json:"url"`
	Directory    string     `json:"directory"`
	OutputPath   string     `json:"outputPath"`
	Status       Status     `json:"status"`
	Bytes        int64      `json:"bytes"`
	Total        int64      `json:"total"`
	Speed        float64    `json:"speed"`
	Error        string     `json:"error,omitempty"`
	ETag         string     `json:"etag,omitempty"`
	LastModified string     `json:"lastModified,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	CompletedAt  *time.Time `json:"completedAt,omitempty"`
}

// HTTPStatusError 表示服务器返回了不可下载的 HTTP 状态码。
type HTTPStatusError struct {
	URL        string
	StatusCode int
	Status     string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("HTTP 请求失败：%s（%s）", e.Status, e.URL)
}

func validStatus(status Status) bool {
	switch status {
	case StatusQueued, StatusDownloading, StatusPaused, StatusCompleted, StatusFailed, StatusCanceled:
		return true
	default:
		return false
	}
}
