package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendWithKeyboard(t *testing.T) {
	var gotReq sendRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true,"result":{"message_id":42}}`)
	}))
	defer srv.Close()

	c := New("tok")
	c.base = srv.URL
	id, err := c.Send(context.Background(), "1", "hi", [][]Button{{
		{Text: "👍", Data: "k:abc"},
		{Text: "✓ Read", Data: "r:chan"},
	}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id != 42 {
		t.Errorf("message id = %d, want 42", id)
	}
	if gotReq.ReplyMarkup == nil || len(gotReq.ReplyMarkup.InlineKeyboard) != 1 || len(gotReq.ReplyMarkup.InlineKeyboard[0]) != 2 {
		t.Fatalf("keyboard not encoded: %+v", gotReq.ReplyMarkup)
	}
	if b := gotReq.ReplyMarkup.InlineKeyboard[0][0]; b.Text != "👍" || b.CallbackData != "k:abc" {
		t.Errorf("button[0] = %+v", b)
	}
}

func TestGetUpdates(t *testing.T) {
	var gotReq getUpdatesRequest
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true,"result":[
			{"update_id":7,"message":{"message_id":3,"from":{"id":99,"username":"corne"},"chat":{"id":99},"text":"/unread"}},
			{"update_id":8,"callback_query":{"id":"cb1","from":{"id":99},"data":"k:abc"}}
		]}`)
	}))
	defer srv.Close()

	c := New("tok")
	c.base = srv.URL
	ups, err := c.GetUpdates(context.Background(), 5, 0)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if gotPath != "/bottok/getUpdates" {
		t.Errorf("path = %q", gotPath)
	}
	if gotReq.Offset != 5 {
		t.Errorf("offset = %d, want 5", gotReq.Offset)
	}
	if len(ups) != 2 {
		t.Fatalf("got %d updates, want 2", len(ups))
	}
	if ups[0].Message == nil || ups[0].Message.From.ID != 99 || ups[0].Message.Text != "/unread" {
		t.Errorf("update[0] message = %+v", ups[0].Message)
	}
	if ups[1].CallbackQuery == nil || ups[1].CallbackQuery.Data != "k:abc" {
		t.Errorf("update[1] callback = %+v", ups[1].CallbackQuery)
	}
}

func TestGetUpdatesPhotoMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// A photo reply: empty text, words in caption, photo as a size ladder.
		io.WriteString(w, `{"ok":true,"result":[
			{"update_id":1,"message":{"message_id":5,"from":{"id":99},"chat":{"id":99},
				"caption":"here you go","reply_to_message":{"message_id":4},
				"photo":[
					{"file_id":"small","width":90,"height":60,"file_size":1000},
					{"file_id":"big","width":1280,"height":853,"file_size":99000}
				]}}
		]}`)
	}))
	defer srv.Close()

	c := New("tok")
	c.base = srv.URL
	ups, err := c.GetUpdates(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if len(ups) != 1 || ups[0].Message == nil {
		t.Fatalf("got %d updates", len(ups))
	}
	m := ups[0].Message
	if m.Text != "" || m.Caption != "here you go" {
		t.Errorf("text=%q caption=%q (caption should carry the words)", m.Text, m.Caption)
	}
	if m.ReplyToMessage == nil || m.ReplyToMessage.MessageID != 4 {
		t.Errorf("reply_to = %+v", m.ReplyToMessage)
	}
	ph := m.LargestPhoto()
	if ph == nil || ph.FileID != "big" {
		t.Errorf("LargestPhoto = %+v, want the highest-resolution size", ph)
	}
}

func TestLargestPhoto(t *testing.T) {
	if (&Message{}).LargestPhoto() != nil {
		t.Error("no photo should yield nil")
	}
	var m *Message
	if m.LargestPhoto() != nil {
		t.Error("nil message should yield nil")
	}
	// Picks by size even when the ladder isn't ordered smallest-first.
	got := (&Message{Photo: []PhotoSize{
		{FileID: "mid", FileSize: 500},
		{FileID: "big", FileSize: 9000},
		{FileID: "small", FileSize: 100},
	}}).LargestPhoto()
	if got == nil || got.FileID != "big" {
		t.Errorf("LargestPhoto = %+v", got)
	}
}

func TestAnswerCallback(t *testing.T) {
	var gotPath string
	var gotReq answerCallbackRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c := New("tok")
	c.base = srv.URL
	if err := c.AnswerCallback(context.Background(), "cb1", "👍 reacted"); err != nil {
		t.Fatalf("AnswerCallback: %v", err)
	}
	if gotPath != "/bottok/answerCallbackQuery" {
		t.Errorf("path = %q", gotPath)
	}
	if gotReq.CallbackQueryID != "cb1" || gotReq.Text != "👍 reacted" {
		t.Errorf("req = %+v", gotReq)
	}
}
