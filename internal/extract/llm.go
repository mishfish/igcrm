package extract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LLM — клієнт до будь-якого OpenAI-сумісного /chat/completions
// (OpenAI, OpenRouter, Ollama, vLLM, LM Studio ...).
type LLM struct {
	BaseURL string // напр. https://api.openai.com/v1, http://ollama:11434/v1
	APIKey  string
	Model   string
	HTTP    *http.Client
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Complete повертає JSON-об'єкт з відповіді моделі.
func (l *LLM) Complete(ctx context.Context, system, user string) (json.RawMessage, error) {
	reqBody, _ := json.Marshal(map[string]any{
		"model":           l.Model,
		"temperature":     0,
		"response_format": map[string]string{"type": "json_object"},
		"messages": []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.BaseURL+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if l.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+l.APIKey)
	}
	hc := l.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 90 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		msg := string(body)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("llm http %d: %s", resp.StatusCode, msg)
	}
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("llm response: %w", err)
	}
	if len(r.Choices) == 0 {
		return nil, errors.New("llm: no choices")
	}
	return extractJSONObject(r.Choices[0].Message.Content)
}

// extractJSONObject дістає перший JSON-об'єкт з тексту (на випадок ```json ... ``` чи пояснень навколо).
func extractJSONObject(s string) (json.RawMessage, error) {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("llm: no json object in %q", truncate(s, 200))
	}
	raw := json.RawMessage(s[start : end+1])
	if !json.Valid(raw) {
		return nil, fmt.Errorf("llm: invalid json %q", truncate(s, 200))
	}
	return raw, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
