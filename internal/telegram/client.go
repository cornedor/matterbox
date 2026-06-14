// Package telegram is a tiny outbound Telegram Bot API client: enough to push a
// text message to a chat. The `matterbox listen` daemon uses it to bridge
// direct mentions / DMs to the user's phone. It has no dependency on the UI or
// store packages so it can be unit-tested against an httptest server.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// apiBase is the Telegram Bot API root. The bot token is appended per request
// (".../bot<token>/<method>").
const apiBase = "https://api.telegram.org"

// requestTimeout bounds a single sendMessage call. Telegram is a remote HTTPS
// service, so this is short relative to the LLM timeout — a slow Telegram
// shouldn't wedge the notify goroutine.
const requestTimeout = 30 * time.Second

// Client sends messages as a Telegram bot. The zero value is not usable; use
// New. Safe for concurrent use (http.Client is).
type Client struct {
	token string
	base  string // overridable in tests; defaults to apiBase
	http  *http.Client
}

// New builds a Client for the given bot token (from @BotFather).
func New(token string) *Client {
	return &Client{token: token, base: apiBase, http: &http.Client{}}
}

type sendRequest struct {
	ChatID                string `json:"chat_id"`
	Text                  string `json:"text"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview"`
}

type apiResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

// SendMessage delivers text to chatID. chatID may be a numeric id ("123456") or
// an @channelusername. Plain text is sent (no parse_mode) so message bodies need
// no Markdown/HTML escaping.
func (c *Client) SendMessage(ctx context.Context, chatID, text string) error {
	if c == nil {
		return fmt.Errorf("telegram: nil client")
	}
	if c.token == "" {
		return fmt.Errorf("telegram: no bot token configured")
	}
	if strings.TrimSpace(chatID) == "" {
		return fmt.Errorf("telegram: no chat_id configured")
	}

	payload, err := json.Marshal(sendRequest{
		ChatID:                chatID,
		Text:                  text,
		DisableWebPagePreview: true,
	})
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	url := fmt.Sprintf("%s/bot%s/sendMessage", strings.TrimRight(c.base, "/"), c.token)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call telegram: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var decoded apiResponse
	_ = json.Unmarshal(body, &decoded)
	if resp.StatusCode != http.StatusOK || !decoded.OK {
		desc := decoded.Description
		if desc == "" {
			desc = strings.TrimSpace(string(body))
		}
		if desc == "" {
			desc = resp.Status
		}
		// Don't echo the URL: it embeds the bot token.
		return fmt.Errorf("telegram sendMessage failed (%s): %s", resp.Status, desc)
	}
	return nil
}
