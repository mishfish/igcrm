// Package tgbot implements a minimal HTTP client for the Telegram Bot API:
// long-polling for updates, sending/editing messages with inline keyboards,
// and answering callback queries.
package tgbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const defaultBaseURL = "https://api.telegram.org"

// Update is a single item returned by getUpdates: either an incoming
// message or a callback from an inline keyboard button press.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// Message is a Telegram message.
type Message struct {
	MessageID int64  `json:"message_id"`
	Chat      Chat   `json:"chat"`
	From      *User  `json:"from,omitempty"`
	Text      string `json:"text"`
}

// Chat identifies a Telegram chat.
type Chat struct {
	ID int64 `json:"id"`
}

// User identifies a Telegram user.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// CallbackQuery is sent when a user taps an inline keyboard button.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data"`
}

// InlineKeyboardButton is one button of an inline keyboard.
type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

// InlineKeyboardMarkup is an inline keyboard attached to a message.
type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

// Client is a minimal Telegram Bot API client.
type Client struct {
	BotToken string
	BaseURL  string
	// HTTP is the client used for all requests, including GetUpdates's long
	// poll. GetUpdates bounds its own request with a per-call context
	// timeout (timeoutSec + 5s headroom), but that can only shorten the
	// effective deadline, never lengthen it past this client's Timeout. If
	// set, HTTP.Timeout must be 0 (unbounded) or exceed the longest
	// timeoutSec+5s ever passed to GetUpdates — a shorter Client-wide
	// Timeout would also cut off the long poll. Left nil, http.DefaultClient
	// is used, which has Timeout == 0.
	HTTP *http.Client
}

// apiResponse mirrors Telegram's generic response envelope.
type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
}

// call marshals payload to JSON, POSTs it to the given Bot API method, and
// decodes the result into out (when out is non-nil). Errors are built only
// from the response body's description field, never from the request URL
// (the bot token lives only in the URL and must never leak into an error).
func (c *Client) call(ctx context.Context, method string, payload any, out any) error {
	base := c.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	url := fmt.Sprintf("%s/bot%s/%s", base, c.BotToken, method)

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("telegram %s: marshal payload: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		// No %w here: on a malformed URL this error embeds the URL itself,
		// which contains the bot token.
		return fmt.Errorf("telegram %s: build request failed", method)
	}
	req.Header.Set("Content-Type", "application/json")

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		// No %w here: a *url.Error from Do() embeds the request URL, which
		// contains the bot token.
		return fmt.Errorf("telegram %s: request failed", method)
	}
	defer resp.Body.Close()

	var apiResp apiResponse
	if decErr := json.NewDecoder(resp.Body).Decode(&apiResp); decErr != nil {
		return fmt.Errorf("telegram %s: decode response: %w", method, decErr)
	}

	if !apiResp.OK || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram %s: %s", method, apiResp.Description)
	}

	if out != nil && len(apiResp.Result) > 0 {
		if err := json.Unmarshal(apiResp.Result, out); err != nil {
			return fmt.Errorf("telegram %s: decode result: %w", method, err)
		}
	}

	return nil
}

// GetUpdates long-polls for new updates starting at offset, waiting up to
// timeoutSec seconds for Telegram to deliver one.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error) {
	payload := struct {
		Offset  int64 `json:"offset"`
		Timeout int   `json:"timeout"`
	}{Offset: offset, Timeout: timeoutSec}

	// Telegram holds the connection open for up to timeoutSec seconds during
	// a long poll, so this request needs extra headroom beyond that.
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second+5*time.Second)
	defer cancel()

	var updates []Update
	if err := c.call(ctx, "getUpdates", payload, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

// SendMessage sends text to chatID, optionally with an inline keyboard, and
// returns the ID of the sent message.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, kb *InlineKeyboardMarkup) (int64, error) {
	payload := struct {
		ChatID      int64                 `json:"chat_id"`
		Text        string                `json:"text"`
		ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
	}{ChatID: chatID, Text: text, ReplyMarkup: kb}

	var result struct {
		MessageID int64 `json:"message_id"`
	}
	if err := c.call(ctx, "sendMessage", payload, &result); err != nil {
		return 0, err
	}
	return result.MessageID, nil
}

// EditMessageText replaces the text (and optionally the inline keyboard) of
// an existing message.
func (c *Client) EditMessageText(ctx context.Context, chatID, messageID int64, text string, kb *InlineKeyboardMarkup) error {
	payload := struct {
		ChatID      int64                 `json:"chat_id"`
		MessageID   int64                 `json:"message_id"`
		Text        string                `json:"text"`
		ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
	}{ChatID: chatID, MessageID: messageID, Text: text, ReplyMarkup: kb}

	return c.call(ctx, "editMessageText", payload, nil)
}

// AnswerCallbackQuery acknowledges a callback query, optionally showing text
// as a toast notification in the Telegram client.
func (c *Client) AnswerCallbackQuery(ctx context.Context, callbackID, text string) error {
	payload := struct {
		CallbackQueryID string `json:"callback_query_id"`
		Text            string `json:"text,omitempty"`
	}{CallbackQueryID: callbackID, Text: text}

	return c.call(ctx, "answerCallbackQuery", payload, nil)
}
