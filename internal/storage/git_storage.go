package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type GitStorageBackend struct {
	repoURL          string
	token            string
	branch           string
	filePath         string
	authKeysFilePath string
	localCacheDir    string
}

func NewGitStorageBackend(repoURL, token, branch, filePath, authKeysFilePath, localCacheDir string) (*GitStorageBackend, error) {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return nil, fmt.Errorf("git storage: repo_url is required")
	}
	if branch == "" {
		branch = "main"
	}
	if filePath == "" {
		filePath = "accounts.json"
	}
	if authKeysFilePath == "" {
		authKeysFilePath = "auth_keys.json"
	}
	if localCacheDir == "" {
		localCacheDir = filepath.Join(os.TempDir(), "chatgpt2api_git_cache")
	}
	_ = os.MkdirAll(localCacheDir, 0o755)
	return &GitStorageBackend{
		repoURL:          repoURL,
		token:            token,
		branch:           branch,
		filePath:         filePath,
		authKeysFilePath: authKeysFilePath,
		localCacheDir:    localCacheDir,
	}, nil
}

func (b *GitStorageBackend) authURL() string {
	if b.token == "" || !strings.HasPrefix(b.repoURL, "https://") {
		return b.repoURL
	}
	return strings.Replace(b.repoURL, "https://", "https://"+b.token+"@", 1)
}

func (b *GitStorageBackend) repoPath() string {
	return filepath.Join(b.localCacheDir, "repo")
}

func (b *GitStorageBackend) cloneOrPull() error {
	repoPath := b.repoPath()
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err == nil {
		cmd := exec.Command("git", "-C", repoPath, "pull", "origin", b.branch)
		if output, err := cmd.CombinedOutput(); err != nil {
			_ = os.RemoveAll(repoPath)
			return b.cloneRepo()
		} else {
			_ = output
		}
		return nil
	}
	return b.cloneRepo()
}

func (b *GitStorageBackend) cloneRepo() error {
	repoPath := b.repoPath()
	_ = os.RemoveAll(repoPath)
	cmd := exec.Command("git", "clone", "--branch", b.branch, "--depth", "1", b.authURL(), repoPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func (b *GitStorageBackend) commitAndPush(message string) error {
	repoPath := b.repoPath()
	cmd := exec.Command("git", "-C", repoPath, "add", ".")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	cmd = exec.Command("git", "-C", repoPath, "diff", "--cached", "--quiet")
	if err := cmd.Run(); err == nil {
		return nil
	}
	cmd = exec.Command("git", "-C", repoPath, "commit", "-m", message)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	cmd = exec.Command("git", "-C", repoPath, "push", "origin", b.branch)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git push failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func (b *GitStorageBackend) readJSON(path string) ([]map[string]any, error) {
	fullPath := filepath.Join(b.repoPath(), filepath.FromSlash(path))
	data, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var result any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	switch typed := result.(type) {
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out, nil
	case map[string]any:
		if items := typed["items"]; items != nil {
			if arr, ok := items.([]any); ok {
				out := make([]map[string]any, 0, len(arr))
				for _, item := range arr {
					if m, ok := item.(map[string]any); ok {
						out = append(out, m)
					}
				}
				return out, nil
			}
		}
		return nil, fmt.Errorf("unexpected git JSON structure")
	default:
		return nil, nil
	}
}

func (b *GitStorageBackend) writeJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	fullPath := filepath.Join(b.repoPath(), filepath.FromSlash(path))
	_ = os.MkdirAll(filepath.Dir(fullPath), 0o755)
	if err := os.WriteFile(fullPath, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return b.commitAndPush("Update " + path)
}

func (b *GitStorageBackend) LoadAccounts() ([]map[string]any, error) {
	if err := b.cloneOrPull(); err != nil {
		return nil, err
	}
	return b.readJSON(b.filePath)
}

func (b *GitStorageBackend) SaveAccounts(accounts []map[string]any) error {
	if err := b.cloneOrPull(); err != nil {
		return err
	}
	items := make([]any, 0, len(accounts))
	for _, acc := range accounts {
		items = append(items, acc)
	}
	return b.writeJSON(b.filePath, items)
}

func (b *GitStorageBackend) LoadAuthKeys() ([]map[string]any, error) {
	if err := b.cloneOrPull(); err != nil {
		return nil, err
	}
	return b.readJSON(b.authKeysFilePath)
}

func (b *GitStorageBackend) SaveAuthKeys(keys []map[string]any) error {
	if err := b.cloneOrPull(); err != nil {
		return err
	}
	return b.writeJSON(b.authKeysFilePath, map[string]any{"items": keys})
}

func (b *GitStorageBackend) HealthCheck() map[string]any {
	result := map[string]any{
		"backend":             "git",
		"repo_url":            maskToken(b.repoURL),
		"branch":              b.branch,
		"file_path":           b.filePath,
		"auth_keys_file_path": b.authKeysFilePath,
	}
	if err := b.cloneOrPull(); err != nil {
		result["status"] = "unhealthy"
		result["error"] = err.Error()
		return result
	}
	result["status"] = "healthy"
	if head, err := os.ReadFile(filepath.Join(b.repoPath(), ".git", "refs", "heads", b.branch)); err == nil {
		result["last_commit"] = strings.TrimSpace(string(head))[:8]
	}
	return result
}

func (b *GitStorageBackend) Info() map[string]any {
	return map[string]any{
		"type":        "git",
		"description": "Git private repository storage",
		"repo_url":    maskToken(b.repoURL),
		"branch":      b.branch,
	}
}

func maskToken(url string) string {
	if !strings.Contains(url, "@") || !strings.Contains(url, "://") {
		return url
	}
	parts := strings.SplitN(url, "://", 2)
	if len(parts) != 2 {
		return url
	}
	rest := strings.SplitN(parts[1], "@", 2)
	if len(rest) != 2 {
		return url
	}
	return parts[0] + "://****@" + rest[1]
}
