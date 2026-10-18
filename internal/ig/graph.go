package ig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const graphBase = "https://graph.instagram.com"

// Client — мінімальний клієнт Instagram API with Instagram Login.
type Client struct {
	Version string
	Token   func(ctx context.Context) (string, error) // береться з БД на кожен запит (токен оновлюється)
	HTTP    *http.Client
	BaseURL string // для тестів; порожньо = graph.instagram.com
}

type GraphError struct {
	Status  int
	Message string
	Code    int
}

func (e *GraphError) Error() string {
	return fmt.Sprintf("graph api %d (code %d): %s", e.Status, e.Code, e.Message)
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return graphBase
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// get виконує GET і декодує JSON. rawURL може бути повним (paging.next) або шляхом.
// Токен іде в query (так вимагає refresh endpoint), тому з помилок URL вирізається.
func (c *Client) get(ctx context.Context, rawURL string, params url.Values, token string, out any) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if !u.IsAbs() {
		u, _ = url.Parse(c.base() + rawURL)
	}
	q := u.Query()
	for k, vs := range params {
		for _, v := range vs {
			q.Set(k, v)
		}
	}
	if token != "" && q.Get("access_token") == "" {
		q.Set("access_token", token)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("GET %s: %w", u.Path, ue.Err) // без query з токеном
		}
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
				Code    int    `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		msg := e.Error.Message
		if msg == "" {
			msg = strings.TrimSpace(string(body))
			if len(msg) > 300 {
				msg = msg[:300]
			}
		}
		return &GraphError{Status: resp.StatusCode, Message: msg, Code: e.Error.Code}
	}
	return json.Unmarshal(body, out)
}

func (c *Client) token(ctx context.Context) (string, error) {
	if c.Token == nil {
		return "", errors.New("no token source")
	}
	return c.Token(ctx)
}

type Profile struct {
	Name     string `json:"name"`
	Username string `json:"username"`
}

// Profile — ім'я та @handle співрозмовника за IGSID.
func (c *Client) Profile(ctx context.Context, igsid string) (Profile, error) {
	var p Profile
	tok, err := c.token(ctx)
	if err != nil {
		return p, err
	}
	err = c.get(ctx, "/"+c.Version+"/"+url.PathEscape(igsid), url.Values{"fields": {"name,username"}}, tok, &p)
	return p, err
}

type Me struct {
	UserID   string `json:"user_id"` // IG professional account id — те саме, що entry.id у вебхуках
	Username string `json:"username"`
}

func (c *Client) Me(ctx context.Context) (Me, error) {
	var m Me
	tok, err := c.token(ctx)
	if err != nil {
		return m, err
	}
	err = c.get(ctx, "/"+c.Version+"/me", url.Values{"fields": {"user_id,username"}}, tok, &m)
	return m, err
}

// Refresh подовжує long-lived токен ще на 60 днів. Токен має бути старшим за 24 години й ще валідним.
func (c *Client) Refresh(ctx context.Context, token string) (string, time.Time, error) {
	var r struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	err := c.get(ctx, "/refresh_access_token", url.Values{"grant_type": {"ig_refresh_token"}}, token, &r)
	if err != nil {
		return "", time.Time{}, err
	}
	if r.AccessToken == "" || r.ExpiresIn <= 0 {
		return "", time.Time{}, errors.New("refresh: empty token in response")
	}
	return r.AccessToken, time.Now().Add(time.Duration(r.ExpiresIn) * time.Second), nil
}

// ---------- Conversations API (для бекфілу старих переписок) ----------

type Participant struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

type ConvMessage struct {
	ID          string      `json:"id"`
	CreatedTime string      `json:"created_time"`
	From        Participant `json:"from"`
	To          struct {
		Data []Participant `json:"data"`
	} `json:"to"`
	Message string `json:"message"`
}

type Conversation struct {
	ID           string `json:"id"`
	Participants struct {
		Data []Participant `json:"data"`
	} `json:"participants"`
	Messages struct {
		Data []ConvMessage `json:"data"`
	} `json:"messages"`
}

type page[T any] struct {
	Data   []T `json:"data"`
	Paging struct {
		Next string `json:"next"`
	} `json:"paging"`
}

// Conversations ітерує всі діалоги з останніми повідомленнями (Meta віддає деталі лише для
// обмеженої кількості останніх повідомлень у кожному діалозі).
func (c *Client) Conversations(ctx context.Context, fn func(Conversation) error) error {
	tok, err := c.token(ctx)
	if err != nil {
		return err
	}
	next := "/" + c.Version + "/me/conversations"
	params := url.Values{
		"platform": {"instagram"},
		"fields":   {"participants,messages{id,created_time,from,to,message}"},
		"limit":    {"25"},
	}
	for next != "" {
		var p page[Conversation]
		if err := c.get(ctx, next, params, tok, &p); err != nil {
			return err
		}
		for _, conv := range p.Data {
			if err := fn(conv); err != nil {
				return err
			}
		}
		next, params = p.Paging.Next, nil // next уже містить усі параметри
	}
	return nil
}

// ParseGraphTime парсить created_time ("2026-10-18T10:09:00+0000").
func ParseGraphTime(s string) time.Time {
	for _, layout := range []string{"2006-01-02T15:04:05-0700", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}
