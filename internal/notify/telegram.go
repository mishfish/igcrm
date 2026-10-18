package notify

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Notifier шле алерти в Telegram; без налаштувань — лише пише в лог.
type Notifier struct {
	BotToken string
	ChatID   string
	Log      *slog.Logger
}

func (n *Notifier) Alert(ctx context.Context, text string) {
	n.Log.Error("ALERT: " + text)
	if n.BotToken == "" || n.ChatID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	form := url.Values{"chat_id": {n.ChatID}, "text": {"igcrm: " + text}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.telegram.org/bot"+n.BotToken+"/sendMessage", strings.NewReader(form.Encode()))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		n.Log.Warn("telegram alert failed") // без err: у ньому URL з токеном бота
		return
	}
	resp.Body.Close()
}
