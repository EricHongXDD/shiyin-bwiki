package download

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultConcurrency      = 4
	defaultProgressInterval = 200 * time.Millisecond
)

type taskState struct {
	task               Task
	active             bool
	cancel             context.CancelFunc
	lastPersistAttempt time.Time
}

// Manager 管理下载队列、worker 和持久化状态。
type Manager struct {
	mu               sync.RWMutex
	cond             *sync.Cond
	persistMu        sync.Mutex
	tasks            map[string]*taskState
	pending          []string
	statePath        string
	client           *http.Client
	onUpdate         func(Task)
	progressInterval time.Duration
	closed           bool
	wg               sync.WaitGroup
}

// NewManager 加载持久化任务并启动 worker。重启前未完成的任务会恢复为 paused。
func NewManager(config Config) (*Manager, error) {
	if strings.TrimSpace(config.StatePath) == "" {
		return nil, errors.New("StatePath 不能为空")
	}

	statePath, err := filepath.Abs(config.StatePath)
	if err != nil {
		return nil, fmt.Errorf("解析状态文件路径：%w", err)
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return nil, fmt.Errorf("创建状态目录：%w", err)
	}

	concurrency := config.Concurrency
	if concurrency <= 0 {
		concurrency = defaultConcurrency
	}
	interval := config.ProgressInterval
	if interval <= 0 {
		interval = defaultProgressInterval
	}
	client := config.Client
	if client == nil {
		client = &http.Client{}
	}
	if !config.AllowPrivateNetwork {
		client = restrictedHTTPClient(client)
	}

	m := &Manager{
		tasks:            make(map[string]*taskState),
		statePath:        statePath,
		client:           client,
		onUpdate:         config.OnUpdate,
		progressInterval: interval,
	}
	m.cond = sync.NewCond(&m.mu)

	changed, err := m.load()
	if err != nil {
		return nil, err
	}
	if changed {
		if err := m.persist(); err != nil {
			return nil, err
		}
	}

	m.wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go m.worker()
	}
	return m, nil
}

// Add 创建单条下载任务。
func (m *Manager) Add(input NewTask) (Task, error) {
	tasks, err := m.AddBatch([]NewTask{input})
	if err != nil {
		return Task{}, err
	}
	return tasks[0], nil
}

// AddBatch 原子地校验并加入一批下载任务。
func (m *Manager) AddBatch(inputs []NewTask) ([]Task, error) {
	if len(inputs) == 0 {
		return []Task{}, nil
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}

	reserved := m.reservedPathsLocked()
	now := time.Now().UTC()
	batchID, err := m.newBatchIDLocked()
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	created := make([]Task, 0, len(inputs))
	for index, input := range inputs {
		task, err := m.prepareTaskLocked(input, reserved, batchID, now)
		if err != nil {
			m.mu.Unlock()
			return nil, fmt.Errorf("第 %d 个任务无效：%w", index+1, err)
		}
		reserved[pathKey(task.OutputPath)] = struct{}{}
		created = append(created, task)
	}
	for _, task := range created {
		copyTask := task
		m.tasks[task.ID] = &taskState{task: copyTask}
	}
	m.mu.Unlock()

	if err := m.persist(); err != nil {
		m.mu.Lock()
		for _, task := range created {
			delete(m.tasks, task.ID)
		}
		m.mu.Unlock()
		_ = m.persist()
		return nil, err
	}

	for _, task := range created {
		m.notify(task)
	}
	m.mu.Lock()
	if !m.closed {
		for _, task := range created {
			m.pending = append(m.pending, task.ID)
		}
		m.cond.Broadcast()
	}
	m.mu.Unlock()
	return created, nil
}

// List 返回按创建时间排序的任务快照。
func (m *Manager) List() []Task {
	m.mu.RLock()
	tasks := m.taskCopiesLocked()
	m.mu.RUnlock()
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].CreatedAt.Equal(tasks[j].CreatedAt) {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})
	return tasks
}

// Get 返回指定任务快照；未找到时返回零值。
func (m *Manager) Get(id string) Task {
	task, _ := m.Lookup(id)
	return task
}

// Lookup 返回指定任务快照以及是否存在。
func (m *Manager) Lookup(id string) (Task, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	state, ok := m.tasks[id]
	if !ok {
		return Task{}, false
	}
	return state.task, true
}

// Pause 暂停排队中或正在下载的任务，并保留 .part 文件。
func (m *Manager) Pause(id string) error {
	var cancel context.CancelFunc
	task, accepted, err := m.changeTask(id, func(state *taskState) error {
		switch state.task.Status {
		case StatusQueued, StatusDownloading:
			state.task.Status = StatusPaused
			state.task.Speed = 0
			state.task.Error = ""
			state.task.UpdatedAt = time.Now().UTC()
			cancel = state.cancel
			return nil
		case StatusPaused:
			cancel = state.cancel
			return nil
		default:
			return fmt.Errorf("%w：无法暂停 %s 任务", ErrInvalidTransition, state.task.Status)
		}
	})
	if !accepted {
		return err
	}
	if cancel != nil {
		cancel()
	}
	m.notify(task)
	return err
}

// Resume 继续已暂停任务，存在部分文件时会尝试断点续传。
func (m *Manager) Resume(id string) error {
	var enqueue bool
	task, accepted, err := m.changeTask(id, func(state *taskState) error {
		if state.task.Status != StatusPaused {
			return fmt.Errorf("%w：只有 paused 任务可以继续", ErrInvalidTransition)
		}
		state.task.Status = StatusQueued
		state.task.Speed = 0
		state.task.Error = ""
		state.task.CompletedAt = nil
		state.task.UpdatedAt = time.Now().UTC()
		enqueue = !state.active
		return nil
	})
	if !accepted {
		return err
	}
	m.notify(task)
	if enqueue {
		m.enqueue(id)
	}
	return err
}

// Retry 重新排队失败或已取消的任务，并复用仍然有效的部分文件。
func (m *Manager) Retry(id string) error {
	task, accepted, err := m.changeTask(id, func(state *taskState) error {
		if state.active || (state.task.Status != StatusFailed && state.task.Status != StatusCanceled) {
			return fmt.Errorf("%w：只有 failed 或 canceled 任务可以重试", ErrInvalidTransition)
		}
		state.task.Status = StatusQueued
		state.task.Speed = 0
		state.task.Error = ""
		state.task.CompletedAt = nil
		state.task.UpdatedAt = time.Now().UTC()
		return nil
	})
	if !accepted {
		return err
	}
	m.notify(task)
	m.enqueue(id)
	return err
}

// Cancel 取消排队中、暂停中或正在下载的任务。
func (m *Manager) Cancel(id string) error {
	var cancel context.CancelFunc
	task, accepted, err := m.changeTask(id, func(state *taskState) error {
		switch state.task.Status {
		case StatusQueued, StatusDownloading, StatusPaused:
			state.task.Status = StatusCanceled
			state.task.Speed = 0
			state.task.Error = ""
			state.task.UpdatedAt = time.Now().UTC()
			cancel = state.cancel
			return nil
		case StatusCanceled:
			cancel = state.cancel
			return nil
		default:
			return fmt.Errorf("%w：无法取消 %s 任务", ErrInvalidTransition, state.task.Status)
		}
	})
	if !accepted {
		return err
	}
	if cancel != nil {
		cancel()
	}
	m.notify(task)
	return err
}

// Remove 从历史记录中移除一个终态任务，不删除已下载文件或 .part 文件。
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	state, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return ErrTaskNotFound
	}
	if state.active || (state.task.Status != StatusCompleted && state.task.Status != StatusFailed && state.task.Status != StatusCanceled) {
		m.mu.Unlock()
		return fmt.Errorf("%w：只能移除 completed、failed 或 canceled 任务", ErrInvalidTransition)
	}
	removed := state.task
	delete(m.tasks, id)
	m.mu.Unlock()
	if err := m.persist(); err != nil {
		m.mu.Lock()
		m.tasks[id] = &taskState{task: removed}
		m.mu.Unlock()
		_ = m.persist()
		return err
	}
	return nil
}

// ClearCompleted 清除所有已完成任务的历史记录，不删除下载文件。
func (m *Manager) ClearCompleted() (int, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return 0, ErrClosed
	}
	removed := make([]Task, 0)
	for id, state := range m.tasks {
		if !state.active && state.task.Status == StatusCompleted {
			removed = append(removed, state.task)
			delete(m.tasks, id)
		}
	}
	m.mu.Unlock()
	if len(removed) == 0 {
		return 0, nil
	}
	if err := m.persist(); err != nil {
		m.mu.Lock()
		for _, task := range removed {
			copyTask := task
			m.tasks[task.ID] = &taskState{task: copyTask}
		}
		m.mu.Unlock()
		_ = m.persist()
		return 0, err
	}
	return len(removed), nil
}

// Close 将所有未完成任务持久化为 paused，并等待 worker 退出。
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	now := time.Now().UTC()
	cancels := make([]context.CancelFunc, 0)
	updates := make([]Task, 0)
	for _, state := range m.tasks {
		if state.task.Status == StatusQueued || state.task.Status == StatusDownloading {
			state.task.Status = StatusPaused
			state.task.Speed = 0
			state.task.Error = ""
			state.task.UpdatedAt = now
			updates = append(updates, state.task)
		}
		if state.cancel != nil {
			cancels = append(cancels, state.cancel)
		}
	}
	m.cond.Broadcast()
	m.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	for _, task := range updates {
		m.notify(task)
	}
	firstErr := m.persist()
	m.wg.Wait()
	if err := m.persist(); firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// changeTask 的 accepted 表示内存状态变更已经被接受。即使随后持久化失败，
// 调用方也必须完成与该状态对应的取消、入队和通知，避免内存状态与副作用分叉。
func (m *Manager) changeTask(id string, change func(*taskState) error) (task Task, accepted bool, err error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Task{}, false, ErrClosed
	}
	state, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return Task{}, false, ErrTaskNotFound
	}
	if err := change(state); err != nil {
		m.mu.Unlock()
		return Task{}, false, err
	}
	task = state.task
	m.mu.Unlock()
	if err := m.persist(); err != nil {
		return task, true, err
	}
	return task, true, nil
}

func (m *Manager) enqueue(id string) {
	m.mu.Lock()
	if !m.closed {
		m.pending = append(m.pending, id)
		m.cond.Signal()
	}
	m.mu.Unlock()
}

func (m *Manager) prepareTaskLocked(input NewTask, reserved map[string]struct{}, batchID string, now time.Time) (Task, error) {
	taskType := strings.ToLower(strings.TrimSpace(input.Type))
	if taskType == "" {
		taskType = TaskTypeAudio
	}
	if taskType != TaskTypeAudio && taskType != TaskTypeText {
		return Task{}, errors.New("任务类型无效")
	}
	var parsedURL *url.URL
	if taskType == TaskTypeAudio {
		var err error
		parsedURL, err = url.Parse(strings.TrimSpace(input.URL))
		if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" || parsedURL.User != nil {
			return Task{}, errors.New("URL 必须是有效的 HTTP 或 HTTPS 地址")
		}
	} else if len(input.Content) > maxTextContentBytes {
		return Task{}, fmt.Errorf("字幕文本不能超过 %d 字节", maxTextContentBytes)
	}
	if strings.TrimSpace(input.Directory) == "" {
		return Task{}, errors.New("下载目录不能为空")
	}
	directory, err := filepath.Abs(input.Directory)
	if err != nil {
		return Task{}, fmt.Errorf("解析下载目录：%w", err)
	}
	if subdirectory := strings.TrimSpace(input.Subdirectory); subdirectory != "" {
		subdirectory = SanitizeDirectoryName(subdirectory)
		if subdirectory == "" || subdirectory == "." || subdirectory == ".." {
			return Task{}, errors.New("角色名不能作为下载目录")
		}
		directory = filepath.Join(directory, subdirectory)
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return Task{}, fmt.Errorf("创建下载目录：%w", err)
	}

	fileName := strings.TrimSpace(input.FileName)
	if fileName == "" {
		if parsedURL != nil {
			fileName = filepath.Base(parsedURL.Path)
		} else {
			fileName = "subtitle.txt"
		}
	}
	fileName = SanitizeFileName(fileName)
	fileName, outputPath, err := chooseAvailablePath(directory, fileName, reserved)
	if err != nil {
		return Task{}, err
	}

	id, err := m.newIDLocked()
	if err != nil {
		return Task{}, err
	}
	canonicalURL := ""
	if parsedURL != nil {
		canonicalURL = parsedURL.String()
	}
	return Task{
		ID:           id,
		BatchID:      batchID,
		Type:         taskType,
		SourceID:     input.SourceID,
		Title:        input.Title,
		Category:     input.Category,
		Language:     input.Language,
		LanguageName: input.LanguageName,
		FileName:     fileName,
		URL:          canonicalURL,
		Directory:    directory,
		OutputPath:   outputPath,
		Content:      input.Content,
		Status:       StatusQueued,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

func (m *Manager) newBatchIDLocked() (string, error) {
	for attempts := 0; attempts < 8; attempts++ {
		buffer := make([]byte, 16)
		if _, err := rand.Read(buffer); err != nil {
			return "", fmt.Errorf("生成下载批次 ID：%w", err)
		}
		batchID := "batch-" + hex.EncodeToString(buffer)
		duplicate := false
		for _, state := range m.tasks {
			if state.task.BatchID == batchID {
				duplicate = true
				break
			}
		}
		if !duplicate {
			return batchID, nil
		}
	}
	return "", errors.New("无法生成唯一下载批次 ID")
}

func (m *Manager) newIDLocked() (string, error) {
	for attempts := 0; attempts < 8; attempts++ {
		buffer := make([]byte, 16)
		if _, err := rand.Read(buffer); err != nil {
			return "", fmt.Errorf("生成任务 ID：%w", err)
		}
		id := hex.EncodeToString(buffer)
		if _, exists := m.tasks[id]; !exists {
			return id, nil
		}
	}
	return "", errors.New("无法生成唯一任务 ID")
}

func (m *Manager) reservedPathsLocked() map[string]struct{} {
	reserved := make(map[string]struct{}, len(m.tasks))
	for _, state := range m.tasks {
		if state.task.OutputPath != "" {
			reserved[pathKey(state.task.OutputPath)] = struct{}{}
		}
	}
	return reserved
}

func (m *Manager) taskCopiesLocked() []Task {
	tasks := make([]Task, 0, len(m.tasks))
	for _, state := range m.tasks {
		tasks = append(tasks, state.task)
	}
	return tasks
}

func (m *Manager) notify(task Task) {
	if m.onUpdate == nil {
		return
	}
	defer func() {
		// 界面回调不应让下载 worker 因 panic 退出。
		_ = recover()
	}()
	m.onUpdate(task)
}
