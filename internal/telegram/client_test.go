package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendMessage(t *testing.T) {
	var gotPath string
	var gotReq sendRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	defer srv.Close()

	c := New("bot-token-123")
	c.base = srv.URL // redirect to the test server

	if err := c.SendMessage(context.Background(), "999", "hello world"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if gotPath != "/botbot-token-123/sendMessage" {
		t.Errorf("path = %q, want token embedded", gotPath)
	}
	if gotReq.ChatID != "999" || gotReq.Text != "hello world" {
		t.Errorf("request = %+v", gotReq)
	}
	if !gotReq.DisableWebPagePreview {
		t.Error("expected web page preview disabled")
	}
}

func TestSendMessageAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"ok":false,"description":"chat not found"}`)
	}))
	defer srv.Close()

	c := New("tok")
	c.base = srv.URL
	err := c.SendMessage(context.Background(), "999", "hi")
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("want API description in error, got %v", err)
	}
	// The bot token must never leak into the error (it's in the URL).
	if strings.Contains(err.Error(), "tok") {
		t.Errorf("error leaks token: %v", err)
	}
}

func TestSendMessageGuards(t *testing.T) {
	if err := New("").SendMessage(context.Background(), "1", "x"); err == nil {
		t.Error("want error for empty token")
	}
	if err := New("tok").SendMessage(context.Background(), "  ", "x"); err == nil {
		t.Error("want error for empty chat id")
	}
}
