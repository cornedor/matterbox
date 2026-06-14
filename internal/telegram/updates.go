package telegram

import (
	"context"
	"time"
)

// User is a Telegram user (sender of a message or callback).
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// Chat is the conversation a message belongs to. For a private chat with the
// bot, ID equals the user's ID.
type Chat struct {
	ID int64 `json:"id"`
}

// Message is an inbound text message (or the message a button is attached to).
type Message struct {
	MessageID      int      `json:"message_id"`
	From           *User    `json:"from"`
	Chat           *Chat    `json:"chat"`
	Text           string   `json:"text"`
	ReplyToMessage *Message `json:"reply_to_message"`
}

// CallbackQuery is an inline-button tap. Data is the button's callback_data.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *User    `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

// Update is one item from getUpdates: exactly one of Message / CallbackQuery is
// set for the kinds we request.
type Update struct {
	UpdateID      int            `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type getUpdatesRequest struct {
	Offset         int      `json:"offset"`
	Timeout        int      `json:"timeout"`
	AllowedUpdates []string `json:"allowed_updates"`
}

type getUpdatesResponse struct {
	OK     bool     `json:"ok"`
	Result []Update `json:"result"`
}

// GetUpdates long-polls for new updates starting at offset (the last seen
// update_id + 1), blocking up to timeoutSec on the server. Only message and
// callback_query updates are requested. The HTTP deadline is the poll timeout
// plus a margin so the long poll isn't cut short by the client.
func (c *Client) GetUpdates(ctx context.Context, offset, timeoutSec int) ([]Update, error) {
	req := getUpdatesRequest{
		Offset:         offset,
		Timeout:        timeoutSec,
		AllowedUpdates: []string{"message", "callback_query"},
	}
	var resp getUpdatesResponse
	deadline := time.Duration(timeoutSec)*time.Second + 15*time.Second
	if err := c.call(ctx, "getUpdates", deadline, req, &resp); err != nil {
		return nil, err
	}
	return resp.Result, nil
}

type answerCallbackRequest struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text"`
}

// AnswerCallback acknowledges a tapped inline button, optionally showing text
// as a small toast in the client. Telegram re-sends an unanswered callback, so
// always answer — even on failure.
func (c *Client) AnswerCallback(ctx context.Context, callbackID, text string) error {
	return c.call(ctx, "answerCallbackQuery", requestTimeout,
		answerCallbackRequest{CallbackQueryID: callbackID, Text: text}, nil)
}
