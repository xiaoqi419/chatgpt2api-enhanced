package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"chatgpt2api/internal/util"
)

var base64DataURIRe = regexp.MustCompile(`data:[\w/.+;-]+;base64,[A-Za-z0-9+/=]+`)

const (
	maxReviewTextLen    = 100000
	truncationMarker    = "\n\u2026[truncated]\u2026\n"
	defaultReviewPrompt = "Determine whether the user request is allowed. Reply only ALLOW or REJECT."
)

type httpError struct {
	code    int
	message string
}

func (e *httpError) Error() string { return e.message }
func (e *httpError) StatusCode() int { return e.code }

func newHTTPError(code int, message string) *httpError {
	return &httpError{code: code, message: message}
}

type ContentFilterService struct {
	sensitiveWords []string
	aiReview       AIContentReviewSettings
	proxyFunc      func() string
	logFunc        func(level string, message string, details map[string]any)
}

type AIContentReviewSettings struct {
	Enabled  bool
	BaseURL  string
	APIKey   string
	Model    string
	Prompt   string
	FailOpen bool
}

func NewContentFilterService(sensitiveWords []string, review AIContentReviewSettings, proxyFunc func() string, logFunc func(string, string, map[string]any)) *ContentFilterService {
	if review.Prompt == "" {
		review.Prompt = defaultReviewPrompt
	}
	if proxyFunc == nil {
		proxyFunc = func() string { return "" }
	}
	if logFunc == nil {
		logFunc = func(_ string, _ string, _ map[string]any) {}
	}
	return &ContentFilterService{
		sensitiveWords: sensitiveWords,
		aiReview:       review,
		proxyFunc:      proxyFunc,
		logFunc:        logFunc,
	}
}

func (s *ContentFilterService) Check(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	for _, word := range s.sensitiveWords {
		if word != "" && strings.Contains(text, word) {
			return newHTTPError(http.StatusBadRequest, "detected sensitive word, task rejected")
		}
	}

	if !s.aiReview.Enabled || s.aiReview.BaseURL == "" || s.aiReview.APIKey == "" || s.aiReview.Model == "" {
		return nil
	}

	return s.performAIReview(text)
}

func (s *ContentFilterService) performAIReview(text string) error {
	reviewText, sanitizeStats := sanitizeForReview(text)
	if sanitizeStats["base64_blocks_stripped"].(int) > 0 || sanitizeStats["truncated_chars"].(int) > 0 {
		s.logFunc("info", "ai_review_text_sanitized", map[string]any{
			"original_text_len":      len(text),
			"review_text_len":        len(reviewText),
			"base64_blocks_stripped": sanitizeStats["base64_blocks_stripped"],
			"truncated_chars":       sanitizeStats["truncated_chars"],
		})
	}

	prompt := s.aiReview.Prompt
	if prompt == "" {
		prompt = defaultReviewPrompt
	}
	content := fmt.Sprintf("%s\n\nUser request:\n%s\n\nReply only ALLOW or REJECT.", prompt, reviewText)

	onFailure := func(event string, detail map[string]any) {
		s.logFunc("warning", event, detail)
		if !s.aiReview.FailOpen {
			panic(newHTTPError(http.StatusServiceUnavailable, "AI review service temporarily unavailable, please try later"))
		}
	}

	status, payload, err := s.callReviewAPI(content)
	if err != nil {
		onFailure("ai_review_request_failed", map[string]any{
			"error":            err.Error(),
			"review_text_len":  len(reviewText),
			"original_text_len": len(text),
		})
		return nil
	}

	decision, err := extractReviewDecision(status, payload)
	if err != nil {
		onFailure("ai_review_malformed_response", map[string]any{
			"status_code":     status,
			"body_preview":    fmt.Sprintf("%v", payload)[:300],
			"review_text_len": len(reviewText),
		})
		return nil
	}

	if isAllowDecision(decision) {
		return nil
	}
	if isRejectDecision(decision) {
		return newHTTPError(http.StatusBadRequest, "AI review rejected this task")
	}

	onFailure("ai_review_ambiguous_decision", map[string]any{
		"decision":        decision[:minInt(len(decision), 100)],
		"review_text_len": len(reviewText),
	})
	return nil
}

func (s *ContentFilterService) callReviewAPI(content string) (int, map[string]any, error) {
	body, _ := json.Marshal(map[string]any{
		"model": s.aiReview.Model,
		"messages": []map[string]any{
			{"role": "user", "content": content},
		},
		"temperature": 0,
	})

	client := &http.Client{}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(s.aiReview.BaseURL, "/")+"/v1/chat/completions", strings.NewReader(string(body)))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.aiReview.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	var payload map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	return resp.StatusCode, payload, nil
}

func extractReviewDecision(status int, payload map[string]any) (string, error) {
	if status != http.StatusOK {
		return "", fmt.Errorf("review API returned status %d", status)
	}
	choices, ok := payload["choices"].([]any)
	if !ok || len(choices) == 0 {
		return "", fmt.Errorf("no choices in review response")
	}
	first, ok := choices[0].(map[string]any)
	if !ok {
		return "", fmt.Errorf("invalid choice format")
	}
	message, ok := first["message"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("no message in review choice")
	}
	content, _ := message["content"].(string)
	return strings.ToLower(strings.TrimSpace(content)), nil
}

func isAllowDecision(decision string) bool {
	for _, prefix := range []string{"allow", "pass", "true", "yes", "通过", "允许", "安全"} {
		if strings.HasPrefix(decision, prefix) {
			return true
		}
	}
	return false
}

func isRejectDecision(decision string) bool {
	for _, prefix := range []string{"reject", "deny", "block", "false", "no", "拒绝", "不允许", "违规", "禁止"} {
		if strings.HasPrefix(decision, prefix) {
			return true
		}
	}
	return false
}

func sanitizeForReview(text string) (string, map[string]any) {
	sanitized := base64DataURIRe.ReplaceAllString(text, "[image]")
	blocksStripped := strings.Count(text, "data:") - strings.Count(sanitized, "data:")
	truncated := 0
	if len(sanitized) > maxReviewTextLen {
		half := (maxReviewTextLen - len(truncationMarker)) / 2
		truncated = len(sanitized) - 2*half
		sanitized = sanitized[:half] + truncationMarker + sanitized[len(sanitized)-half:]
	}
	stats := map[string]any{
		"base64_blocks_stripped": blocksStripped,
		"truncated_chars":        truncated,
	}
	return sanitized, stats
}

func RequestShape(values ...any) map[string]int {
	stats := map[string]int{
		"response_message_items":     0,
		"input_image_parts":          0,
		"image_url_parts":            0,
		"image_parts":                0,
		"data_url_images":            0,
		"remote_image_urls":          0,
		"literal_image_placeholders": 0,
	}
	for _, value := range values {
		walkRequestShape(value, stats, "")
	}
	out := map[string]int{}
	for key, count := range stats {
		if count > 0 {
			out[key] = count
		}
	}
	return out
}

func walkRequestShape(value any, stats map[string]int, key string) {
	switch typed := value.(type) {
	case string:
		lower := strings.ToLower(typed)
		if strings.Contains(lower, "<image>") {
			stats["literal_image_placeholders"]++
		}
		if strings.HasPrefix(lower, "data:image/") {
			stats["data_url_images"]++
		} else if (key == "image_url" || key == "url") && strings.HasPrefix(lower, "http") {
			stats["remote_image_urls"]++
		}
	case []any:
		for _, item := range typed {
			walkRequestShape(item, stats, "")
		}
	case map[string]any:
		itemType := util.Clean(typed["type"])
		switch itemType {
		case "message":
			stats["response_message_items"]++
		case "input_image":
			stats["input_image_parts"]++
		case "image_url":
			stats["image_url_parts"]++
		case "image":
			stats["image_parts"]++
		}
		for childKey, child := range typed {
			walkRequestShape(child, stats, childKey)
		}
	}
}

