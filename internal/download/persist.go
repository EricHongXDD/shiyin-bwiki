package download

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const stateVersion = 1

type persistedState struct {
	Version int    `json:"version"`
	Tasks   []Task `json:"tasks"`
}

func (m *Manager) load() (bool, error) {
	data, err := os.ReadFile(m.statePath)
	if errors.Is(err, os.ErrNotExist) {
		// 上次进程可能恰好在替换窗口退出，优先恢复明确的同名备份。
		backupPath := m.statePath + ".bak"
		backupData, backupErr := os.ReadFile(backupPath)
		if errors.Is(backupErr, os.ErrNotExist) {
			return false, nil
		}
		if backupErr != nil {
			return false, fmt.Errorf("读取下载状态备份：%w", backupErr)
		}
		if renameErr := os.Rename(backupPath, m.statePath); renameErr != nil {
			return false, fmt.Errorf("恢复下载状态备份：%w", renameErr)
		}
		data = backupData
		err = nil
	}
	if err != nil {
		return false, fmt.Errorf("读取下载状态：%w", err)
	}
	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return false, fmt.Errorf("解析下载状态 JSON：%w", err)
	}
	if state.Version != 0 && state.Version != stateVersion {
		return false, fmt.Errorf("不支持的下载状态版本：%d", state.Version)
	}

	changed := state.Version != stateVersion
	now := time.Now().UTC()
	loadedPaths := make(map[string]struct{}, len(state.Tasks))
	for index, task := range state.Tasks {
		if task.ID == "" {
			return false, fmt.Errorf("下载状态中的第 %d 个任务缺少 ID", index+1)
		}
		if _, duplicate := m.tasks[task.ID]; duplicate {
			return false, fmt.Errorf("下载状态包含重复任务 ID：%s", task.ID)
		}
		if !validStatus(task.Status) {
			return false, fmt.Errorf("任务 %s 包含未知状态：%s", task.ID, task.Status)
		}
		validatedTask, taskChanged, validationErr := validateLoadedTask(task)
		if validationErr != nil {
			// 状态文件属于可被手工编辑的外部输入。危险路径或 URL 只丢弃对应记录，
			// 不能让它在用户点击“重试”时写出下载目录，也不应阻止整个应用启动。
			changed = true
			continue
		}
		task = validatedTask
		if taskChanged {
			changed = true
		}
		outputKey := pathKey(task.OutputPath)
		if _, duplicate := loadedPaths[outputKey]; duplicate {
			// 两个 worker 不能共享同一个目标或 .part 文件。
			changed = true
			continue
		}
		loadedPaths[outputKey] = struct{}{}
		if task.Status == StatusQueued || task.Status == StatusDownloading {
			task.Status = StatusPaused
			task.Error = ""
			changed = true
		}
		if task.Speed != 0 {
			task.Speed = 0
			changed = true
		}
		if task.Bytes < 0 {
			task.Bytes = 0
			changed = true
		}
		if task.Total < 0 {
			task.Total = 0
			changed = true
		}
		if task.CreatedAt.IsZero() {
			task.CreatedAt = now
			changed = true
		}
		if task.UpdatedAt.IsZero() {
			task.UpdatedAt = task.CreatedAt
			changed = true
		}
		copyTask := task
		m.tasks[task.ID] = &taskState{task: copyTask}
	}
	return changed, nil
}

func validateLoadedTask(task Task) (Task, bool, error) {
	changed := false
	taskType := strings.ToLower(strings.TrimSpace(task.Type))
	if taskType == "" {
		taskType = TaskTypeAudio
		task.Type = taskType
		changed = true
	}
	if taskType != TaskTypeAudio && taskType != TaskTypeText {
		return Task{}, false, errors.New("任务类型无效")
	}
	if len(task.Content) > maxTextContentBytes {
		return Task{}, false, errors.New("字幕文本过大")
	}
	if taskType == TaskTypeAudio {
		rawURL := strings.TrimSpace(task.URL)
		parsedURL, err := url.Parse(rawURL)
		if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" || parsedURL.User != nil {
			return Task{}, false, errors.New("URL 不是安全的 HTTP 或 HTTPS 地址")
		}
		canonicalURL := parsedURL.String()
		if task.URL != canonicalURL {
			task.URL = canonicalURL
			changed = true
		}
	}

	if strings.TrimSpace(task.Directory) == "" || !filepath.IsAbs(task.Directory) {
		return Task{}, false, errors.New("下载目录不是绝对路径")
	}
	directory := filepath.Clean(task.Directory)
	if directory != task.Directory {
		task.Directory = directory
		changed = true
	}

	fileName := strings.TrimSpace(task.FileName)
	if fileName == "" || fileName != filepath.Base(fileName) || SanitizeFileName(fileName) != fileName {
		return Task{}, false, errors.New("文件名不是安全的单级名称")
	}
	if task.FileName != fileName {
		task.FileName = fileName
		changed = true
	}
	expectedOutput := filepath.Join(directory, fileName)
	if task.OutputPath == "" {
		task.OutputPath = expectedOutput
		changed = true
	} else if !filepath.IsAbs(task.OutputPath) || pathKey(task.OutputPath) != pathKey(expectedOutput) {
		return Task{}, false, errors.New("目标文件路径不在下载目录中")
	} else if task.OutputPath != expectedOutput {
		task.OutputPath = expectedOutput
		changed = true
	}
	return task, changed, nil
}

func (m *Manager) persist() error {
	m.persistMu.Lock()
	defer m.persistMu.Unlock()

	m.mu.RLock()
	tasks := m.taskCopiesLocked()
	m.mu.RUnlock()
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].CreatedAt.Equal(tasks[j].CreatedAt) {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})
	data, err := json.MarshalIndent(persistedState{Version: stateVersion, Tasks: tasks}, "", "  ")
	if err != nil {
		return fmt.Errorf("编码下载状态：%w", err)
	}
	data = append(data, '\n')

	directory := filepath.Dir(m.statePath)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("创建下载状态目录：%w", err)
	}
	temporary, err := os.CreateTemp(directory, ".download-state-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时状态文件：%w", err)
	}
	temporaryPath := temporary.Name()
	keepTemporary := true
	defer func() {
		_ = temporary.Close()
		if keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("设置临时状态文件权限：%w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("写入下载状态：%w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("同步下载状态：%w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("关闭临时状态文件：%w", err)
	}
	backupPath := m.statePath + ".bak"
	stateExists := false
	if _, err := os.Lstat(m.statePath); err == nil {
		stateExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查原下载状态文件：%w", err)
	}
	if stateExists {
		// Windows 不能可靠地用 Rename 覆盖现有文件，先将唯一目标移到同目录备份。
		if _, err := os.Lstat(backupPath); err == nil {
			if err := os.Remove(backupPath); err != nil {
				return fmt.Errorf("清理旧下载状态备份：%w", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("检查下载状态备份：%w", err)
		}
		if err := os.Rename(m.statePath, backupPath); err != nil {
			return fmt.Errorf("备份原下载状态：%w", err)
		}
	}
	if err := os.Rename(temporaryPath, m.statePath); err != nil {
		if stateExists {
			if rollbackErr := os.Rename(backupPath, m.statePath); rollbackErr != nil {
				return fmt.Errorf("替换下载状态文件：%v；回滚也失败：%w", err, rollbackErr)
			}
		}
		return fmt.Errorf("替换下载状态文件：%w", err)
	}
	keepTemporary = false
	if stateExists {
		// 新状态已经提交后，备份清理失败不能再向调用方报告事务失败。
		// 杀毒软件短暂占用 .bak 时允许它保留；下一次持久化会再次清理。
		_ = os.Remove(backupPath)
	}
	return nil
}
