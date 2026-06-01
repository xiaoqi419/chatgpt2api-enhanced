package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

var cacheableTextKeys = map[string]struct{}{
	"frequency_penalty":    {},
	"max_completion_tokens": {},
	"max_tokens":           {},
	"metadata":             {},
	"model":                {},
	"presence_penalty":     {},
	"reasoning_effort":     {},
	"response_format":      {},
	"seed":                 {},
	"stop":                 {},
	"temperature":          {},
	"tool_choice":          {},
	"tools":                {},
	"top_p":                {},
	"user":                 {},
}

type ChatCompletionCache struct {
	mu       sync.RWMutex
	entries  map[string]cacheEntry
	inflight map[string]*inflightCall
}

type cacheEntry struct {
	expiresAt time.Time
	value     any
}

type inflightCall struct {
	cond  *sync.Cond
	done  bool
	value any
	err   error
}

func NewChatCompletionCache() *ChatCompletionCache {
	return &ChatCompletionCache{
		entries:  map[string]cacheEntry{},
		inflight: map[string]*inflightCall{},
	}
}

func (c *ChatCompletionCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[string]cacheEntry{}
	c.inflight = map[string]*inflightCall{}
}

func jsonSafe(value any) any {
	switch typed := value.(type) {
	case []byte:
		sum := sha256.Sum256(typed)
		return map[string]any{"__bytes_sha256__": hex.EncodeToString(sum[:16]), "length": len(typed)}
	case map[string]any:
		out := map[string]any{}
		for k, v := range typed {
			out[k] = jsonSafe(v)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, v := range typed {
			out[i] = jsonSafe(v)
		}
		return out
	default:
		return value
	}
}

func CanonicalBody(body map[string]any, messages []map[string]any, stream bool) map[string]any {
	payload := map[string]any{}
	for key := range cacheableTextKeys {
		if val, ok := body[key]; ok {
			payload[key] = val
		}
	}
	payload["messages"] = messages
	payload["stream"] = stream
	return payload
}

func CacheKey(body map[string]any, messages []map[string]any, stream bool) string {
	encoded, _ := json.Marshal(jsonSafe(CanonicalBody(body, messages, stream)))
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func messageSignature(message map[string]any) string {
	data, _ := json.Marshal(jsonSafe(message))
	return string(data)
}

func NormalizeTextMessages(messages []map[string]any, normalize bool, dropAssistant bool, dropAdjacentDupes bool) []map[string]any {
	if !normalize {
		return messages
	}
	normalized := make([]map[string]any, 0, len(messages))
	var prevSig string
	for _, msg := range messages {
		role, _ := msg["role"].(string)
		if dropAssistant && role == "assistant" {
			continue
		}
		sig := messageSignature(msg)
		if dropAdjacentDupes && sig == prevSig {
			continue
		}
		normalized = append(normalized, msg)
		prevSig = sig
	}
	return normalized
}

func (c *ChatCompletionCache) GetOrCompute(key string, enabled bool, ttlSeconds int, dedupe bool, compute func() (any, error)) (any, error) {
	if !enabled || ttlSeconds <= 0 {
		return compute()
	}

	now := time.Now()
	c.mu.Lock()
	c.pruneLocked(now, 1000)
	if entry, ok := c.entries[key]; ok && entry.expiresAt.After(now) {
		c.mu.Unlock()
		return deepCopyAny(entry.value), nil
	}

	var call *inflightCall
	var owner bool
	if dedupe {
		if existing, ok := c.inflight[key]; ok {
			call = existing
			owner = false
		} else {
			call = &inflightCall{cond: sync.NewCond(&sync.Mutex{})}
			c.inflight[key] = call
			owner = true
		}
	} else {
		call = &inflightCall{cond: sync.NewCond(&sync.Mutex{})}
		owner = true
	}
	c.mu.Unlock()

	if !owner {
		call.cond.L.Lock()
		for !call.done {
			call.cond.Wait()
		}
		call.cond.L.Unlock()
		if call.err != nil {
			return nil, call.err
		}
		return deepCopyAny(call.value), nil
	}

	value, err := compute()
	c.mu.Lock()
	delete(c.inflight, key)

	if err != nil {
		c.mu.Unlock()
		call.cond.L.Lock()
		call.err = err
		call.done = true
		call.cond.Broadcast()
		call.cond.L.Unlock()
		return nil, err
	}

	c.entries[key] = cacheEntry{
		expiresAt: time.Now().Add(time.Duration(ttlSeconds) * time.Second),
		value:     deepCopyAny(value),
	}
	c.pruneLocked(time.Now(), 1000)
	c.mu.Unlock()

	call.cond.L.Lock()
	call.value = deepCopyAny(value)
	call.done = true
	call.cond.Broadcast()
	call.cond.L.Unlock()
	return value, nil
}

func (c *ChatCompletionCache) pruneLocked(now time.Time, maxEntries int) {
	for key, entry := range c.entries {
		if !entry.expiresAt.After(now) {
			delete(c.entries, key)
		}
	}
	for len(c.entries) > maxEntries {
		var oldestKey string
		var oldestTime time.Time
		for key, entry := range c.entries {
			if oldestKey == "" || entry.expiresAt.Before(oldestTime) {
				oldestKey = key
				oldestTime = entry.expiresAt
			}
		}
		delete(c.entries, oldestKey)
	}
}

func deepCopyAny(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var out any
	json.Unmarshal(data, &out)
	return out
}
