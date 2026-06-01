package service

import (
	"fmt"
	"sync"

	"chatgpt2api/internal/storage"
	"chatgpt2api/internal/util"
)

type ImageTagsService struct {
	mu    sync.RWMutex
	store storage.JSONDocumentBackend
}

func NewImageTagsService(store storage.JSONDocumentBackend) *ImageTagsService {
	return &ImageTagsService{store: store}
}

func (s *ImageTagsService) List(ownerID string) ([]map[string]any, error) {
	data, err := s.load()
	if err != nil {
		return nil, err
	}
	root, _ := data.(map[string]any)
	if root == nil {
		return []map[string]any{}, nil
	}
	users, _ := root["users"].(map[string]any)
	if users == nil {
		return []map[string]any{}, nil
	}
	items, _ := users[ownerID].([]any)
	if items == nil {
		return []map[string]any{}, nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *ImageTagsService) Upsert(ownerID string, tag map[string]any) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.load()
	if err != nil {
		return nil, err
	}
	root, _ := data.(map[string]any)
	if root == nil {
		root = map[string]any{}
	}
	users, _ := root["users"].(map[string]any)
	if users == nil {
		users = map[string]any{}
		root["users"] = users
	}
	items, _ := users[ownerID].([]any)
	if items == nil {
		items = []any{}
	}

	tagID := util.Clean(tag["id"])
	if tagID == "" {
		tagID = util.NewHex(8)
		tag["id"] = tagID
	}
	tag["created_at"] = firstNonEmpty(util.Clean(tag["created_at"]), util.NowISO())
	tag["updated_at"] = util.NowISO()

	found := false
	for i, item := range items {
		if m, ok := item.(map[string]any); ok && util.Clean(m["id"]) == tagID {
			items[i] = tag
			found = true
			break
		}
	}
	if !found {
		items = append(items, tag)
	}
	users[ownerID] = items

	if err := s.save(root); err != nil {
		return nil, err
	}
	return tag, nil
}

func (s *ImageTagsService) Delete(ownerID, tagID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.load()
	if err != nil {
		return false, err
	}
	root, _ := data.(map[string]any)
	if root == nil {
		return false, nil
	}
	users, _ := root["users"].(map[string]any)
	if users == nil {
		return false, nil
	}
	items, _ := users[ownerID].([]any)
	if items == nil {
		return false, nil
	}

	newItems := make([]any, 0, len(items))
	deleted := false
	for _, item := range items {
		if m, ok := item.(map[string]any); ok && util.Clean(m["id"]) == tagID {
			deleted = true
			continue
		}
		newItems = append(newItems, item)
	}
	if !deleted {
		return false, nil
	}
	users[ownerID] = newItems

	if err := s.save(root); err != nil {
		return false, err
	}
	return true, nil
}

func (s *ImageTagsService) load() (any, error) {
	if s.store == nil {
		return map[string]any{}, nil
	}
	value, err := s.store.LoadJSONDocument("image_tags.json")
	if value == nil || err != nil {
		return map[string]any{}, nil
	}
	return value, err
}

func (s *ImageTagsService) save(value any) error {
	if s.store == nil {
		return fmt.Errorf("no document store available")
	}
	return s.store.SaveJSONDocument("image_tags.json", value)
}
