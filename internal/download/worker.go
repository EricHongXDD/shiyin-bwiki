package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type downloadResult struct {
	bytes        int64
	total        int64
	etag         string
	lastModified string
	completed    bool
	err          error
}

const progressPersistInterval = time.Second

const maxTextContentBytes = 1 << 20

func (m *Manager) worker() {
	defer m.wg.Done()
	for {
		id, task, ctx, ok := m.startNext()
		if !ok {
			return
		}
		if err := m.persist(); err != nil {
			m.finish(id, downloadResult{
				bytes:        task.Bytes,
				total:        task.Total,
				etag:         task.ETag,
				lastModified: task.LastModified,
				err:          fmt.Errorf("保存下载启动状态：%w", err),
			})
			continue
		}
		m.notify(task)
		result := m.download(ctx, task)
		m.finish(id, result)
	}
}

func (m *Manager) startNext() (string, Task, context.Context, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for {
		for len(m.pending) == 0 && !m.closed {
			m.cond.Wait()
		}
		if m.closed {
			return "", Task{}, nil, false
		}
		id := m.pending[0]
		m.pending[0] = ""
		m.pending = m.pending[1:]
		state, exists := m.tasks[id]
		if !exists || state.active || state.task.Status != StatusQueued {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		state.active = true
		state.cancel = cancel
		state.task.Status = StatusDownloading
		state.task.Speed = 0
		state.task.Error = ""
		state.task.UpdatedAt = time.Now().UTC()
		return id, state.task, ctx, true
	}
}

func (m *Manager) finish(id string, result downloadResult) {
	m.mu.Lock()
	state, exists := m.tasks[id]
	if !exists {
		m.mu.Unlock()
		return
	}
	state.active = false
	state.cancel = nil
	if result.bytes >= 0 {
		state.task.Bytes = result.bytes
	}
	if result.total >= 0 {
		state.task.Total = result.total
	}
	state.task.ETag = result.etag
	state.task.LastModified = result.lastModified
	state.task.Speed = 0
	state.task.UpdatedAt = time.Now().UTC()

	shouldEnqueue := false
	switch state.task.Status {
	case StatusQueued:
		// 用户在取消旧请求结束前点击了继续，待旧 worker 退出后再安全入队。
		shouldEnqueue = !m.closed
	case StatusDownloading:
		if result.completed {
			state.task.Status = StatusCompleted
			state.task.Error = ""
			completedAt := time.Now().UTC()
			state.task.CompletedAt = &completedAt
		} else {
			state.task.Status = StatusFailed
			if result.err != nil {
				state.task.Error = result.err.Error()
			} else {
				state.task.Error = "下载未完成"
			}
		}
	case StatusPaused, StatusCanceled:
		// 状态由用户操作决定，不能被迟到的网络错误覆盖。
	}
	task := state.task
	m.mu.Unlock()
	if err := m.persist(); err != nil {
		log.Printf("保存下载任务 %s 的终态失败：%v", id, err)
	}
	m.notify(task)
	if shouldEnqueue {
		m.enqueue(id)
	}
}

func (m *Manager) updateProgress(id string, downloaded, total int64, speed float64, etag, lastModified string) {
	_, _ = m.storeProgress(id, downloaded, total, speed, etag, lastModified, false)
}

func (m *Manager) checkpointProgress(id string, downloaded, total int64, etag, lastModified string) error {
	updated, err := m.storeProgress(id, downloaded, total, 0, etag, lastModified, true)
	if err != nil {
		return err
	}
	if !updated {
		return errors.New("任务已不处于可完成的下载状态")
	}
	return nil
}

func (m *Manager) storeProgress(id string, downloaded, total int64, speed float64, etag, lastModified string, forcePersist bool) (bool, error) {
	m.mu.Lock()
	state, exists := m.tasks[id]
	if !exists || state.task.Status != StatusDownloading {
		m.mu.Unlock()
		return false, nil
	}
	now := time.Now().UTC()
	validatorChanged := state.task.ETag != etag || state.task.LastModified != lastModified
	state.task.Bytes = downloaded
	state.task.Total = total
	state.task.Speed = speed
	state.task.ETag = etag
	state.task.LastModified = lastModified
	state.task.UpdatedAt = now
	shouldPersist := forcePersist || validatorChanged || state.lastPersistAttempt.IsZero() || now.Sub(state.lastPersistAttempt) >= progressPersistInterval
	if shouldPersist {
		state.lastPersistAttempt = now
	}
	task := state.task
	m.mu.Unlock()
	// 界面进度保持高频更新；完整状态文件只按较低频率事务写盘。
	m.notify(task)
	if shouldPersist {
		if err := m.persist(); err != nil {
			if !forcePersist {
				log.Printf("保存下载任务 %s 的进度失败：%v", id, err)
			}
			return true, err
		}
	}
	return true, nil
}

func (m *Manager) download(ctx context.Context, task Task) downloadResult {
	result := downloadResult{
		bytes:        task.Bytes,
		total:        task.Total,
		etag:         task.ETag,
		lastModified: task.LastModified,
	}
	if task.Type == TaskTypeText {
		return m.downloadText(ctx, task, result)
	}
	if err := os.MkdirAll(task.Directory, 0o755); err != nil {
		result.err = fmt.Errorf("创建下载目录：%w", err)
		return result
	}

	partPath := task.OutputPath + ".part"
	offset, err := partialSize(partPath)
	if err != nil {
		result.err = err
		return result
	}
	result.bytes = offset

	if completed, knownBytes, err := recoverRenamedDownload(task, partPath); err != nil {
		result.err = err
		return result
	} else if completed {
		result.bytes = knownBytes
		result.total = knownBytes
		result.completed = true
		return result
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, task.URL, nil)
	if err != nil {
		result.err = fmt.Errorf("创建 HTTP 请求：%w", err)
		return result
	}
	request.Header.Set("Accept-Encoding", "identity")
	if offset > 0 {
		switch {
		case isStrongETag(task.ETag):
			request.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
			request.Header.Set("If-Range", task.ETag)
		case strings.TrimSpace(task.LastModified) != "":
			request.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
			request.Header.Set("If-Range", task.LastModified)
		default:
			// 没有强 ETag 或 Last-Modified 时无法证明分段仍属于同一资源，
			// 必须从头下载，避免把两个版本静默拼接成一个损坏文件。
			offset = 0
			result.bytes = 0
		}
	}

	response, err := m.client.Do(request)
	if err != nil {
		result.err = fmt.Errorf("发送 HTTP 请求：%w", err)
		return result
	}
	defer response.Body.Close()

	appendPart := false
	total := int64(0)
	totalKnown := false
	switch response.StatusCode {
	case http.StatusOK:
		// 返回 200 表示服务器忽略 Range 或资源校验已变化，必须安全地从头写入。
		offset = 0
		result.bytes = 0
		result.etag = response.Header.Get("ETag")
		result.lastModified = response.Header.Get("Last-Modified")
		if response.ContentLength >= 0 {
			total = response.ContentLength
			totalKnown = true
		}
	case http.StatusPartialContent:
		start, end, contentTotal, known, parseErr := parseContentRange(response.Header.Get("Content-Range"))
		if parseErr != nil {
			result.err = parseErr
			return result
		}
		if start != offset {
			result.err = fmt.Errorf("服务器返回的 Content-Range 起点为 %d，期望 %d", start, offset)
			return result
		}
		if response.ContentLength >= 0 && response.ContentLength != end-start+1 {
			result.err = fmt.Errorf("服务器返回的分段长度不一致：Content-Length=%d，Range=%d", response.ContentLength, end-start+1)
			return result
		}
		responseETag := response.Header.Get("ETag")
		responseLastModified := response.Header.Get("Last-Modified")
		if offset > 0 && validatorsConflict(task, responseETag, responseLastModified) {
			// 错误实现的服务器可能在 If-Range 失配后仍返回 206，绝不能把新资源追加到旧分段。
			if err := os.Truncate(partPath, 0); err != nil {
				result.err = fmt.Errorf("资源校验器变化后清空旧分段：%w", err)
				return result
			}
			result.bytes = 0
			result.total = 0
			result.etag = responseETag
			result.lastModified = responseLastModified
			m.updateProgress(task.ID, 0, 0, 0, result.etag, result.lastModified)
			result.err = errors.New("服务器返回的资源校验器已变化，旧分段已清空，请重试")
			return result
		}
		appendPart = offset > 0
		if responseETag != "" {
			result.etag = responseETag
		}
		if responseLastModified != "" {
			result.lastModified = responseLastModified
		}
		if known {
			total = contentTotal
			totalKnown = true
		} else if response.ContentLength >= 0 {
			total = offset + response.ContentLength
			totalKnown = true
		}
	case http.StatusRequestedRangeNotSatisfiable:
		if offset > 0 && rangeLengthMatches(response.Header.Get("Content-Range"), offset, task.Total) {
			result.bytes = offset
			result.total = offset
			if value := response.Header.Get("ETag"); value != "" {
				result.etag = value
			}
			if value := response.Header.Get("Last-Modified"); value != "" {
				result.lastModified = value
			}
			// 先提交最终进度，再把 .part 原子改名。若恰好在改名后崩溃，
			// 下次启动可凭已落盘的 Bytes/Total 识别完整目标文件。
			if err := m.checkpointProgress(task.ID, offset, offset, result.etag, result.lastModified); err != nil {
				result.err = fmt.Errorf("保存下载完成检查点：%w", err)
				return result
			}
			if err := completePartial(partPath, task.OutputPath, offset); err != nil {
				result.err = err
				return result
			}
			result.completed = true
			return result
		}
		result.err = &HTTPStatusError{URL: task.URL, StatusCode: response.StatusCode, Status: response.Status}
		return result
	default:
		result.err = &HTTPStatusError{URL: task.URL, StatusCode: response.StatusCode, Status: response.Status}
		return result
	}

	publicTotal := int64(0)
	if totalKnown {
		publicTotal = total
	}
	result.total = publicTotal

	flags := os.O_CREATE | os.O_WRONLY
	if appendPart {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	partial, err := os.OpenFile(partPath, flags, 0o644)
	if err != nil {
		result.err = fmt.Errorf("打开部分下载文件：%w", err)
		return result
	}
	partialOpen := true
	defer func() {
		if partialOpen {
			_ = partial.Close()
		}
	}()
	if appendPart {
		info, statErr := partial.Stat()
		if statErr != nil {
			result.err = fmt.Errorf("检查部分下载文件：%w", statErr)
			return result
		}
		if info.Size() != offset {
			result.err = fmt.Errorf("部分下载文件在请求期间发生变化：当前 %d 字节，期望 %d 字节", info.Size(), offset)
			return result
		}
	}
	// 先完成创建、截断或追加校验，再持久化新的校验器和字节数。
	// 否则若 O_TRUNC 失败，旧分段可能被错误地标记成新资源的一部分。
	m.updateProgress(task.ID, offset, publicTotal, 0, result.etag, result.lastModified)

	buffer := make([]byte, 64*1024)
	downloaded := offset
	lastReportedBytes := downloaded
	lastReportedAt := time.Now()
	for {
		count, readErr := response.Body.Read(buffer)
		if count > 0 {
			if totalKnown && downloaded+int64(count) > total {
				result.bytes = downloaded
				result.err = fmt.Errorf("服务器发送的数据超过声明长度 %d", total)
				return result
			}
			written, writeErr := partial.Write(buffer[:count])
			if writeErr != nil {
				result.bytes = downloaded + int64(written)
				result.err = fmt.Errorf("写入部分下载文件：%w", writeErr)
				return result
			}
			if written != count {
				result.bytes = downloaded + int64(written)
				result.err = io.ErrShortWrite
				return result
			}
			downloaded += int64(written)
			result.bytes = downloaded
			now := time.Now()
			if elapsed := now.Sub(lastReportedAt); elapsed >= m.progressInterval {
				speed := float64(downloaded-lastReportedBytes) / elapsed.Seconds()
				m.updateProgress(task.ID, downloaded, publicTotal, speed, result.etag, result.lastModified)
				lastReportedAt = now
				lastReportedBytes = downloaded
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			result.err = fmt.Errorf("读取响应数据：%w", readErr)
			return result
		}
	}
	if totalKnown && downloaded != total {
		result.err = fmt.Errorf("下载长度不完整：收到 %d 字节，期望 %d 字节", downloaded, total)
		return result
	}
	if err := partial.Sync(); err != nil {
		result.err = fmt.Errorf("同步部分下载文件：%w", err)
		return result
	}
	if err := partial.Close(); err != nil {
		result.err = fmt.Errorf("关闭部分下载文件：%w", err)
		return result
	}
	partialOpen = false
	finalTotal := publicTotal
	if !totalKnown {
		finalTotal = downloaded
	}
	result.bytes = downloaded
	result.total = finalTotal
	// 最终进度必须先于重命名可靠落盘，以关闭“文件已完成但任务仍显示未完成”的崩溃窗口。
	if err := m.checkpointProgress(task.ID, downloaded, finalTotal, result.etag, result.lastModified); err != nil {
		result.err = fmt.Errorf("保存下载完成检查点：%w", err)
		return result
	}
	if err := completePartial(partPath, task.OutputPath, downloaded); err != nil {
		result.err = err
		return result
	}
	result.completed = true
	return result
}

func (m *Manager) downloadText(ctx context.Context, task Task, result downloadResult) downloadResult {
	data := []byte(task.Content)
	result.total = int64(len(data))
	if err := ctx.Err(); err != nil {
		result.err = err
		return result
	}
	if err := os.MkdirAll(task.Directory, 0o755); err != nil {
		result.err = fmt.Errorf("创建字幕目录：%w", err)
		return result
	}
	partPath := task.OutputPath + ".part"
	if info, err := os.Stat(task.OutputPath); err == nil {
		if info.Mode().IsRegular() && info.Size() == int64(len(data)) {
			if _, partErr := os.Stat(partPath); errors.Is(partErr, os.ErrNotExist) {
				result.bytes = int64(len(data))
				result.completed = true
				return result
			}
		}
		result.err = errors.New("目标字幕文件已存在，为避免覆盖已停止任务")
		return result
	} else if !errors.Is(err, os.ErrNotExist) {
		result.err = fmt.Errorf("检查目标字幕文件：%w", err)
		return result
	}

	file, err := os.OpenFile(partPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		result.err = fmt.Errorf("创建字幕临时文件：%w", err)
		return result
	}
	written, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		result.err = fmt.Errorf("写入字幕文件：%w", writeErr)
		return result
	}
	if err := ctx.Err(); err != nil {
		result.err = err
		return result
	}
	if written != len(data) {
		result.err = fmt.Errorf("写入字幕文件不完整：收到 %d 字节，期望 %d 字节", written, len(data))
		return result
	}
	if err := m.checkpointProgress(task.ID, int64(written), int64(len(data)), "", ""); err != nil {
		result.err = fmt.Errorf("保存字幕完成检查点：%w", err)
		return result
	}
	if err := completePartial(partPath, task.OutputPath, int64(written)); err != nil {
		result.err = err
		return result
	}
	result.bytes = int64(written)
	result.completed = true
	return result
}

func partialSize(path string) (int64, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("检查部分下载文件：%w", err)
	}
	if !info.Mode().IsRegular() {
		return 0, errors.New("部分下载路径不是普通文件")
	}
	return info.Size(), nil
}

func recoverRenamedDownload(task Task, partPath string) (bool, int64, error) {
	outputInfo, err := os.Lstat(task.OutputPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, fmt.Errorf("检查目标文件：%w", err)
	}
	if !outputInfo.Mode().IsRegular() {
		return false, 0, errors.New("目标下载路径已存在且不是普通文件")
	}
	if task.Total > 0 && task.Bytes == task.Total && outputInfo.Size() == task.Total {
		if _, partErr := os.Lstat(partPath); errors.Is(partErr, os.ErrNotExist) {
			return true, task.Total, nil
		}
	}
	return false, 0, errors.New("目标下载文件已存在，为避免覆盖已停止任务")
}

func completePartial(partPath, outputPath string, expectedSize int64) error {
	info, err := os.Lstat(partPath)
	if err != nil {
		return fmt.Errorf("检查待完成文件：%w", err)
	}
	if !info.Mode().IsRegular() || info.Size() != expectedSize {
		return fmt.Errorf("待完成文件长度不匹配：当前 %d 字节，期望 %d 字节", info.Size(), expectedSize)
	}
	if _, err := os.Lstat(outputPath); err == nil {
		return errors.New("目标下载文件已存在，为避免覆盖已停止任务")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查目标下载文件：%w", err)
	}
	if err := os.Rename(partPath, outputPath); err != nil {
		return fmt.Errorf("完成下载文件重命名：%w", err)
	}
	return nil
}

func parseContentRange(header string) (start, end, total int64, totalKnown bool, err error) {
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(strings.ToLower(header), "bytes ") {
		return 0, 0, 0, false, errors.New("206 响应缺少有效的 Content-Range")
	}
	value := strings.TrimSpace(header[len("bytes "):])
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] == "*" {
		return 0, 0, 0, false, fmt.Errorf("无效的 Content-Range：%q", header)
	}
	rangeParts := strings.Split(parts[0], "-")
	if len(rangeParts) != 2 {
		return 0, 0, 0, false, fmt.Errorf("无效的 Content-Range：%q", header)
	}
	start, err = strconv.ParseInt(strings.TrimSpace(rangeParts[0]), 10, 64)
	if err != nil || start < 0 {
		return 0, 0, 0, false, fmt.Errorf("无效的 Content-Range 起点：%q", header)
	}
	end, err = strconv.ParseInt(strings.TrimSpace(rangeParts[1]), 10, 64)
	if err != nil || end < start {
		return 0, 0, 0, false, fmt.Errorf("无效的 Content-Range 终点：%q", header)
	}
	if strings.TrimSpace(parts[1]) == "*" {
		return start, end, 0, false, nil
	}
	total, err = strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil || total <= end {
		return 0, 0, 0, false, fmt.Errorf("无效的 Content-Range 总长度：%q", header)
	}
	return start, end, total, true, nil
}

func rangeLengthMatches(header string, offset, rememberedTotal int64) bool {
	header = strings.TrimSpace(header)
	if strings.HasPrefix(strings.ToLower(header), "bytes */") {
		value := strings.TrimSpace(header[len("bytes */"):])
		total, err := strconv.ParseInt(value, 10, 64)
		return err == nil && total == offset
	}
	return rememberedTotal > 0 && rememberedTotal == offset
}

func validatorsConflict(task Task, responseETag, responseLastModified string) bool {
	if isStrongETag(task.ETag) && responseETag != "" {
		return task.ETag != responseETag
	}
	if task.LastModified != "" && responseLastModified != "" {
		return task.LastModified != responseLastModified
	}
	return false
}

func isStrongETag(value string) bool {
	value = strings.TrimSpace(value)
	return len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"'
}
