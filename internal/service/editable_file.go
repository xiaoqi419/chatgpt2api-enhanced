package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"chatgpt2api/internal/util"
)

const (
	editableFileModel           = "gpt-5-5-thinking"
	editableFileThinkingEffort  = "extended"
	editableFileTimeoutSecs     = 1200
	editableFilePollInterval    = 5 * time.Second
	editableFileClientVersion   = "prod-bede35f9dcd856d080e012478f0c1031faa2588e"
	editableFileClientBuildNum  = "6631702"

	editableFileTaskQueued   = "queued"
	editableFileTaskRunning  = "running"
	editableFileTaskSuccess  = "success"
	editableFileTaskError    = "error"

	editableFileKindPPT = "ppt"
	editableFileKindPSD = "psd"

	editableFilePPTPrompt = `I need you to create an editable PPT based on user requirements; do NOT ask further questions; fill in content style, layout, color scheme, content structure and page information yourself and execute directly. Overall flow:
1. Generate a beautiful product intro PPT with 5-6 pages using image generation
2. Split all images and shape materials into separate PNGs, one file per material, no omissions, for direct recovery in PPT, no text
3. Use all the images and shape materials to restore the first presentation PPT in editable format, insert main sections separately, text must be editable
Finally just output one PPT file and one zip file containing all materials.`

	editableFilePSDPrompt = `Generate this image, split this poster into several images including background; don't change positions so I can use them directly in PS without dragging; white background, no fake transparent background. Then combine the split images into one PSD file; remove white background; keep each layer's relative position; preserve each element layer; output only the PSD file and a zip file of each layer.`
)

var editableFilePlanTypes = []string{"Plus", "Team", "Pro", "Enterprise"}

type EditableFileTaskService struct {
	mu     sync.RWMutex
	tasks  map[string]map[string]any
	path   string
	dataDir string

	accountLookup func() ([]map[string]any, error)
	runFileExport func(ctx context.Context, token, kind, prompt string, images []string, outputDir string) (map[string]any, error)
}

type editableFileTaskResult struct {
	ConversationID string `json:"conversation_id"`
	PrimaryURL     string `json:"primary_url"`
	ZipURL         string `json:"zip_url"`
}

func NewEditableFileTaskService(dataDir string, accountLookup func() ([]map[string]any, error), runExport func(ctx context.Context, token, kind, prompt string, images []string, outputDir string) (map[string]any, error)) *EditableFileTaskService {
	path := filepath.Join(dataDir, "editable_file_tasks.json")
	s := &EditableFileTaskService{
		tasks:         map[string]map[string]any{},
		path:          path,
		dataDir:       dataDir,
		accountLookup: accountLookup,
		runFileExport: runExport,
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	s.tasks = s.load()
	s.recoverUnfinished()
	return s
}

func (s *EditableFileTaskService) Submit(identity map[string]any, kind, prompt string, base64Images []string, baseURL string) (map[string]any, error) {
	taskID := util.Clean(identity["client_task_id"])
	if taskID == "" {
		taskID = util.NewHex(16)
	}
	ownerID := util.Clean(identity["id"])
	if ownerID == "" {
		ownerID = "anonymous"
	}
	key := editableFileTaskKey(ownerID, taskID)

	s.mu.Lock()
	if existing, ok := s.tasks[key]; ok {
		s.mu.Unlock()
		return publicEditableFileTask(existing), nil
	}
	now := util.NowISO()
	ts := float64(time.Now().UnixMilli()) / 1000
	task := map[string]any{
		"id":         taskID,
		"owner_id":   ownerID,
		"status":     editableFileTaskQueued,
		"kind":       kind,
		"model":      editableFileModel,
		"created_at": now,
		"updated_at": now,
		"created_ts": ts,
		"updated_ts": ts,
	}
	if kind == "" {
		kind = editableFileKindPPT
		task["kind"] = kind
	}
	s.tasks[key] = task
	public := publicEditableFileTask(task)
	s.saveLocked()
	s.mu.Unlock()

	go s.runTask(key, kind, prompt, base64Images, identity, baseURL)
	return public, nil
}

func (s *EditableFileTaskService) List(identity map[string]any, taskIDs []string) map[string]any {
	ownerID := util.Clean(identity["id"])
	if ownerID == "" {
		ownerID = "anonymous"
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(taskIDs) > 0 {
		var items []map[string]any
		var missing []string
		for _, tid := range taskIDs {
			tid = strings.TrimSpace(tid)
			if tid == "" {
				continue
			}
			if task, ok := s.tasks[taskKey(ownerID, tid)]; ok {
				items = append(items, publicEditableFileTask(task))
			} else {
				missing = append(missing, tid)
			}
		}
		return map[string]any{"items": items, "missing_ids": missing}
	}

	var items []map[string]any
	for _, task := range s.tasks {
		if util.Clean(task["owner_id"]) == ownerID {
			items = append(items, publicEditableFileTask(task))
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		return util.Clean(items[i]["updated_at"]) > util.Clean(items[j]["updated_at"])
	})
	return map[string]any{"items": items, "missing_ids": []string{}}
}

func (s *EditableFileTaskService) PublicFilePath(relativePath string) (string, error) {
	relPath := strings.TrimPrefix(relativePath, "/")
	base := filepath.Join(s.dataDir, "files")
	fullPath, err := filepath.Abs(filepath.Join(base, filepath.FromSlash(relPath)))
	if err != nil {
		return "", err
	}
	absBase, _ := filepath.Abs(base)
	if !strings.HasPrefix(fullPath, absBase+string(filepath.Separator)) && fullPath != absBase {
		return "", fmt.Errorf("path traversal detected: %s", relativePath)
	}
	if _, err := os.Stat(fullPath); err != nil {
		return "", err
	}
	return fullPath, nil
}

func (s *EditableFileTaskService) runTask(key, kind, prompt string, base64Images []string, identity map[string]any, baseURL string) {
	started := time.Now()
	token := ""
	accountEmail := ""

	s.updateTask(key, map[string]any{"status": editableFileTaskRunning, "error": "", "started_ts": float64(started.UnixMilli()) / 1000})

	defer func() {
		if r := recover(); r != nil {
			s.updateTask(key, map[string]any{
				"status":        editableFileTaskError,
				"error":         fmt.Sprintf("panic: %v", r),
				"account_email": accountEmail,
				"ended_ts":      float64(time.Now().UnixMilli()) / 1000,
			})
		}
	}()

	token, accountEmail, err := s.pickFileAccount()
	if err != nil {
		s.updateTask(key, map[string]any{
			"status":        editableFileTaskError,
			"error":         err.Error(),
			"account_email": accountEmail,
			"ended_ts":      float64(time.Now().UnixMilli()) / 1000,
		})
		return
	}

	outputDir := filepath.Join(s.dataDir, "files", kind, strings.SplitN(key, ":", 2)[1])
	result, err := s.runFileExport(context.Background(), token, kind, prompt, base64Images, outputDir)
	if err != nil {
		s.updateTask(key, map[string]any{
			"status":        editableFileTaskError,
			"error":         err.Error(),
			"account_email": accountEmail,
			"ended_ts":      float64(time.Now().UnixMilli()) / 1000,
		})
		return
	}

	s.updateTask(key, map[string]any{
		"status":        editableFileTaskSuccess,
		"result":        result,
		"account_email": accountEmail,
		"error":         "",
		"ended_ts":      float64(time.Now().UnixMilli()) / 1000,
	})
}

func (s *EditableFileTaskService) pickFileAccount() (string, string, error) {
	var accounts []map[string]any
	var err error
	if s.accountLookup != nil {
		accounts, err = s.accountLookup()
		if err != nil {
			return "", "", err
		}
	}
	var candidates []map[string]any
	for _, acc := range accounts {
		if util.Clean(acc["access_token"]) == "" {
			continue
		}
		status := util.Clean(acc["status"])
		if status == "禁用" || status == "异常" {
			continue
		}
		planType := util.Clean(acc["planType"])
		for _, allowed := range editableFilePlanTypes {
			if planType == allowed {
				candidates = append(candidates, acc)
				break
			}
		}
	}
	if len(candidates) == 0 {
		return "", "", fmt.Errorf("no available Plus/Team/Pro account for editable file generation")
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return util.Clean(candidates[i]["last_used_at"]) < util.Clean(candidates[j]["last_used_at"])
	})
	return util.Clean(candidates[0]["access_token"]), util.Clean(candidates[0]["email"]), nil
}

func (s *EditableFileTaskService) updateTask(key string, updates map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task, ok := s.tasks[key]; ok {
		for k, v := range updates {
			task[k] = v
		}
		task["updated_at"] = util.NowISO()
		task["updated_ts"] = float64(time.Now().UnixMilli()) / 1000
		s.saveLocked()
	}
}

func (s *EditableFileTaskService) load() map[string]map[string]any {
	tasks := map[string]map[string]any{}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return tasks
	}
	var raw map[string]any
	if json.Unmarshal(data, &raw) != nil {
		return tasks
	}
	items := util.AsMapSlice(raw["tasks"])
	for _, item := range items {
		taskID := util.Clean(item["id"])
		owner := util.Clean(item["owner_id"])
		if taskID == "" || owner == "" {
			continue
		}
		kind := util.Clean(item["kind"])
		if kind != editableFileKindPSD {
			kind = editableFileKindPPT
		}
		task := map[string]any{
			"id":         taskID,
			"owner_id":   owner,
			"status":     firstNonEmpty(util.Clean(item["status"]), editableFileTaskError),
			"kind":       kind,
			"created_at": firstNonEmpty(util.Clean(item["created_at"]), util.NowISO()),
			"updated_at": firstNonEmpty(util.Clean(item["updated_at"]), util.NowISO()),
			"created_ts": floatValue(item, "created_ts"),
			"updated_ts": floatValue(item, "updated_ts"),
		}
		for _, field := range []string{"result", "error", "started_ts", "ended_ts"} {
			if item[field] != nil {
				task[field] = item[field]
			}
		}
		tasks[editableFileTaskKey(owner, taskID)] = task
	}
	return tasks
}

func (s *EditableFileTaskService) saveLocked() {
	items := make([]map[string]any, 0, len(s.tasks))
	for _, task := range s.tasks {
		items = append(items, task)
	}
	sort.SliceStable(items, func(i, j int) bool {
		return util.Clean(items[i]["updated_at"]) > util.Clean(items[j]["updated_at"])
	})
	data, _ := json.Marshal(map[string]any{"tasks": items})
	tmpPath := s.path + ".tmp"
	_ = os.WriteFile(tmpPath, append(data, '\n'), 0o644)
	_ = os.Rename(tmpPath, s.path)
}

func (s *EditableFileTaskService) recoverUnfinished() {
	unfinished := []string{editableFileTaskQueued, editableFileTaskRunning}
	changed := false
	for _, task := range s.tasks {
		status := util.Clean(task["status"])
		if status == unfinished[0] || status == unfinished[1] {
			task["status"] = editableFileTaskError
			task["error"] = "service restarted, task interrupted"
			task["ended_ts"] = float64(time.Now().UnixMilli()) / 1000
			task["updated_at"] = util.NowISO()
			task["updated_ts"] = float64(time.Now().UnixMilli()) / 1000
			changed = true
		}
	}
	if changed {
		s.saveLocked()
	}
}

func editableFileTaskKey(owner, taskID string) string {
	return owner + ":" + taskID
}

func publicEditableFileTask(task map[string]any) map[string]any {
	out := map[string]any{
		"id":              task["id"],
		"taskId":          task["id"],
		"status":          task["status"],
		"kind":            task["kind"],
		"created_at":      task["created_at"],
		"updated_at":      task["updated_at"],
		"elapsed_seconds": elapsedSeconds(task),
	}
	for _, key := range []string{"result", "error"} {
		if task[key] != nil {
			out[key] = task[key]
		}
	}
	return out
}

func elapsedSeconds(task map[string]any) int {
	start := floatValue(task, "started_ts")
	if start == 0 {
		start = floatValue(task, "created_ts")
	}
	end := floatValue(task, "ended_ts")
	if end == 0 {
		end = float64(time.Now().UnixMilli()) / 1000
	}
	return maxInt(0, int(end-start))
}

func floatValue(data map[string]any, key string) float64 {
	switch v := data[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	default:
		return 0
	}
}
