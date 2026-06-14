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

// Button is one inline-keyboard button: a label and the callback_data the bot
// receives when it is tapped (Telegram caps callback_data at 64 bytes).
type Button struct {
	Text string
	Data string
}

type inlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type replyMarkup struct {
	InlineKeyboard [][]inlineButton `json:"inline_keyboard"`
}

type sendRequest struct {
	ChatID                string       `json:"chat_id"`
	Text                  string       `json:"text"`
	DisableWebPagePreview bool         `json:"disable_web_page_preview"`
	ReplyMarkup           *replyMarkup `json:"reply_markup,omitempty"`
}

type sendResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Result      struct {
		MessageID int `json:"message_id"`
	} `json:"result"`
}

// SendMessage delivers plain text to chatID with no inline keyboard. Thin
// wrapper over Send for callers that don't need the returned message id.
func (c *Client) SendMessage(ctx context.Context, chatID, text string) error {
	_, err := c.Send(ctx, chatID, text, nil)
	return err
}

// Send delivers text to chatID with an optional inline keyboard and returns the
// sent message's id (used to correlate later replies). chatID may be a numeric
// id ("123456") or an @channelusername. Plain text (no parse_mode) so bodies
// need no Markdown/HTML escaping.
func (c *Client) Send(ctx context.Context, chatID, text string, keyboard [][]Button) (int, error) {
	if c == nil {
		return 0, fmt.Errorf("telegram: nil client")
	}
	if c.token == "" {
		return 0, fmt.Errorf("telegram: no bot token configured")
	}
	if strings.TrimSpace(chatID) == "" {
		return 0, fmt.Errorf("telegram: no chat_id configured")
	}

	req := sendRequest{ChatID: chatID, Text: text, DisableWebPagePreview: true}
	if len(keyboard) > 0 {
		rm := &replyMarkup{}
		for _, row := range keyboard {
			var r []inlineButton
			for _, b := range row {
				r = append(r, inlineButton{Text: b.Text, CallbackData: b.Data})
			}
			rm.InlineKeyboard = append(rm.InlineKeyboard, r)
		}
		req.ReplyMarkup = rm
	}

	var resp sendResponse
	if err := c.call(ctx, "sendMessage", requestTimeout, req, &resp); err != nil {
		return 0, err
	}
	if !resp.OK {
		return 0, fmt.Errorf("telegram sendMessage: %s", firstNonEmpty(resp.Description, "not ok"))
	}
	return resp.Result.MessageID, nil
}

// call POSTs reqBody as JSON to the given Bot API method and decodes the
// response into out (which may be nil). timeout bounds the single call. The bot
// token lives in the URL, so it is never included in returned errors.
func (c *Client) call(ctx context.Context, method string, timeout time.Duration, reqBody, out any) error {
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	url := fmt.Sprintf("%s/bot%s/%s", strings.TrimRight(c.base, "/"), c.token, method)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call telegram %s: %w", method, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		var e sendResponse
		_ = json.Unmarshal(body, &e)
		// Don't echo the URL: it embeds the bot token.
		return fmt.Errorf("telegram %s failed (%s): %s", method, resp.Status,
			firstNonEmpty(e.Description, strings.TrimSpace(string(body)), resp.Status))
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decode %s response: %w", method, err)
		}
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
