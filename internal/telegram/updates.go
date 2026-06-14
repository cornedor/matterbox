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

// PhotoSize is one rendition of a photo. Telegram delivers a photo as a ladder
// of sizes (thumbnail … original); the largest is the full image. See
// (*Message).LargestPhoto.
type PhotoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int    `json:"file_size"`
}

// Document is a non-photo file attachment: an image sent "as a file", a PDF, a
// voice note, etc. Unlike a photo it carries its own filename and mime type.
type Document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int    `json:"file_size"`
}

// Message is an inbound message (or the message a button is attached to). A
// photo/document message has an empty Text — the user's words ride in Caption.
type Message struct {
	MessageID      int         `json:"message_id"`
	From           *User       `json:"from"`
	Chat           *Chat       `json:"chat"`
	Text           string      `json:"text"`
	Caption        string      `json:"caption"`
	Photo          []PhotoSize `json:"photo"`
	Document       *Document   `json:"document"`
	ReplyToMessage *Message    `json:"reply_to_message"`
}

// LargestPhoto returns the highest-resolution PhotoSize on the message, or nil
// when it carries no photo. Telegram orders the ladder smallest-first, but we
// pick by file size so we don't depend on that ordering.
func (m *Message) LargestPhoto() *PhotoSize {
	if m == nil || len(m.Photo) == 0 {
		return nil
	}
	best := &m.Photo[0]
	for i := range m.Photo {
		if m.Photo[i].FileSize > best.FileSize {
			best = &m.Photo[i]
		}
	}
	return best
}

// CallbackQuery is an inline-button tap. Data is the button's callback_data.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *User    `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

// ReactionType is one reaction on a message. Type is "emoji" (Emoji holds the
// unicode reaction), "custom_emoji", or "paid"; we only act on "emoji".
type ReactionType struct {
	Type  string `json:"type"`
	Emoji string `json:"emoji"`
}

// MessageReactionUpdated reports a change to a user's reactions on a message.
// OldReaction/NewReaction are the full sets before/after the change, so the
// difference is the add/remove.
type MessageReactionUpdated struct {
	Chat        Chat           `json:"chat"`
	MessageID   int            `json:"message_id"`
	User        *User          `json:"user"`
	OldReaction []ReactionType `json:"old_reaction"`
	NewReaction []ReactionType `json:"new_reaction"`
}

// Update is one item from getUpdates: exactly one of the fields below is set for
// the kinds we request.
type Update struct {
	UpdateID        int                     `json:"update_id"`
	Message         *Message                `json:"message"`
	CallbackQuery   *CallbackQuery          `json:"callback_query"`
	MessageReaction *MessageReactionUpdated `json:"message_reaction"`
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
		Offset:  offset,
		Timeout: timeoutSec,
		// message_reaction must be opted into explicitly; it carries the user's
		// native emoji reactions on the bot's notifications.
		AllowedUpdates: []string{"message", "callback_query", "message_reaction"},
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
