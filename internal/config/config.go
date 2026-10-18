package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	ListenAddr  string
	DatabaseURL string

	// Instagram / Meta
	IGAppSecret        string // Instagram app secret (API setup with Instagram login → Business login settings)
	IGVerifyToken      string // будь-який рядок, той самий вводиш у дашборді Meta
	IGAccessToken      string // long-lived токен з дашборду; використовується лише для першого запуску
	IGForceToken       bool   // перезаписати токен у БД значенням з env
	IGAPIVersion       string
	IGRefreshThreshold time.Duration

	// LLM: openai (OpenAI-сумісний: OpenAI, OpenRouter, Ollama, ...) або claude (Claude API)
	LLMProvider     string
	LLMBaseURL      string
	LLMAPIKey       string
	LLMModel        string
	ExtractDebounce time.Duration
	ExtractHistory  int

	// Telegram-алерти (опційно)
	TGBotToken string
	TGChatID   string
}

func Load() (Config, error) {
	c := Config{
		ListenAddr:         env("LISTEN_ADDR", ":8080"),
		DatabaseURL:        os.Getenv("DATABASE_URL"),
		IGAppSecret:        os.Getenv("IG_APP_SECRET"),
		IGVerifyToken:      os.Getenv("IG_VERIFY_TOKEN"),
		IGAccessToken:      os.Getenv("IG_ACCESS_TOKEN"),
		IGForceToken:       os.Getenv("IG_ACCESS_TOKEN_FORCE") == "1",
		IGAPIVersion:       env("IG_API_VERSION", "v23.0"),
		IGRefreshThreshold: dur("IG_REFRESH_THRESHOLD", 15*24*time.Hour),
		LLMProvider:        env("LLM_PROVIDER", "openai"),
		LLMBaseURL:         strings.TrimRight(os.Getenv("LLM_BASE_URL"), "/"),
		LLMAPIKey:          os.Getenv("LLM_API_KEY"),
		LLMModel:           os.Getenv("LLM_MODEL"),
		ExtractDebounce:    dur("EXTRACT_DEBOUNCE", 3*time.Minute),
		ExtractHistory:     intEnv("EXTRACT_HISTORY", 30),
		TGBotToken:         os.Getenv("TG_BOT_TOKEN"),
		TGChatID:           os.Getenv("TG_CHAT_ID"),
	}
	var missing []string
	for k, v := range map[string]string{
		"DATABASE_URL":    c.DatabaseURL,
		"IG_APP_SECRET":   c.IGAppSecret,
		"IG_VERIFY_TOKEN": c.IGVerifyToken,
	} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("missing env: %s", strings.Join(missing, ", "))
	}
	return c, nil
}

// LLMEnabled — екстракція вмикається, коли задано модель і ключ/endpoint для обраного провайдера.
func (c Config) LLMEnabled() bool {
	if c.LLMModel == "" {
		return false
	}
	if c.LLMProvider == "claude" {
		return c.LLMAPIKey != ""
	}
	return c.LLMBaseURL != ""
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func dur(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func intEnv(k string, def int) int {
	var n int
	if _, err := fmt.Sscan(os.Getenv(k), &n); err == nil && n > 0 {
		return n
	}
	return def
}
