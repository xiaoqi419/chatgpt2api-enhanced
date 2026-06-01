package service

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"chatgpt2api/internal/util"
)

type BackupService struct {
	mu        sync.Mutex
	dataDir   string
	backupDir string
	maxCount  int
}

func NewBackupService(dataDir, backupDir string, maxCount int) *BackupService {
	if backupDir == "" {
		backupDir = filepath.Join(dataDir, "backups")
	}
	if maxCount <= 0 {
		maxCount = 10
	}
	_ = os.MkdirAll(backupDir, 0o755)
	return &BackupService{
		dataDir:   dataDir,
		backupDir: backupDir,
		maxCount:  maxCount,
	}
}

func (s *BackupService) Create() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	timestamp := time.Now().UTC().Format("20060102-150405")
	filename := fmt.Sprintf("backup-%s.tar.gz", timestamp)
	fullPath := filepath.Join(s.backupDir, filename)

	file, err := os.Create(fullPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	gzWriter := gzip.NewWriter(file)
	defer gzWriter.Close()

	tarWriter := tar.NewWriter(gzWriter)
	defer tarWriter.Close()

	err = filepath.Walk(s.dataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == s.backupDir || strings.HasPrefix(path, s.backupDir+string(filepath.Separator)) {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(s.dataDir, path)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tarWriter, f)
		return err
	})
	if err != nil {
		_ = os.Remove(fullPath)
		return "", err
	}

	s.purgeOldLocked()
	return fullPath, nil
}

func (s *BackupService) List() ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.backupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []map[string]any{}, nil
		}
		return nil, err
	}

	var items []map[string]any
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		items = append(items, map[string]any{
			"name":     entry.Name(),
			"size":     info.Size(),
			"modified": info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return util.Clean(items[i]["modified"]) > util.Clean(items[j]["modified"])
	})
	return items, nil
}

func (s *BackupService) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cleanName := filepath.Base(name)
	if cleanName != name || cleanName == "." || cleanName == ".." {
		return fmt.Errorf("invalid backup name")
	}
	path := filepath.Join(s.backupDir, cleanName)
	return os.Remove(path)
}

func (s *BackupService) GetPath(name string) (string, error) {
	cleanName := filepath.Base(name)
	if cleanName != name || cleanName == "." || cleanName == ".." {
		return "", fmt.Errorf("invalid backup name")
	}
	path := filepath.Join(s.backupDir, cleanName)
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	return path, nil
}

func (s *BackupService) purgeOldLocked() {
	entries, _ := os.ReadDir(s.backupDir)
	if len(entries) <= s.maxCount {
		return
	}
	type entryWithTime struct {
		name    string
		modTime time.Time
	}
	var items []entryWithTime
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, entryWithTime{e.Name(), info.ModTime()})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].modTime.Before(items[j].modTime)
	})
	for len(items) > s.maxCount {
		_ = os.Remove(filepath.Join(s.backupDir, items[0].name))
		items = items[1:]
	}
}
