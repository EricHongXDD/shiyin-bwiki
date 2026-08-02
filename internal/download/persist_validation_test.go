package download

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDropsUnsafeAndDuplicateTaskPaths(t *testing.T) {
	directory := t.TempDir()
	statePath := filepath.Join(directory, "tasks.json")
	now := time.Now().UTC()
	valid := Task{
		ID:         "valid",
		FileName:   "voice.ogg",
		URL:        "https://example.com/voice.ogg",
		Directory:  directory,
		OutputPath: filepath.Join(directory, "voice.ogg"),
		Status:     StatusCompleted,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	unsafeURL := valid
	unsafeURL.ID = "unsafe-url"
	unsafeURL.FileName = "unsafe-url.ogg"
	unsafeURL.OutputPath = filepath.Join(directory, unsafeURL.FileName)
	unsafeURL.URL = "https://user:password@example.com/voice.ogg"
	escapingPath := valid
	escapingPath.ID = "escaping-path"
	escapingPath.FileName = "escape.ogg"
	escapingPath.OutputPath = filepath.Join(directory, "..", "escape.ogg")
	duplicatePath := valid
	duplicatePath.ID = "duplicate-path"

	data, err := json.Marshal(persistedState{
		Version: stateVersion,
		Tasks:   []Task{valid, unsafeURL, escapingPath, duplicatePath},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	manager := newTestManager(t, Config{StatePath: statePath, Concurrency: 1})
	defer manager.Close()
	tasks := manager.List()
	if len(tasks) != 1 || tasks[0].ID != valid.ID {
		t.Fatalf("安全校验后任务 = %#v", tasks)
	}

	persistedData, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted persistedState
	if err := json.Unmarshal(persistedData, &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted.Tasks) != 1 || persisted.Tasks[0].ID != valid.ID {
		t.Fatalf("持久化清理后任务 = %#v", persisted.Tasks)
	}
}
