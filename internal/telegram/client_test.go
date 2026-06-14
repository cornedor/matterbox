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

func TestFetchFile(t *testing.T) {
	const want = "the image bytes"
	var getFilePath, downloadPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			getFilePath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"ok":true,"result":{"file_path":"photos/file_42.jpg","file_size":15}}`)
		default:
			downloadPath = r.URL.Path
			io.WriteString(w, want)
		}
	}))
	defer srv.Close()

	c := New("tok")
	c.base = srv.URL
	data, fpath, err := c.FetchFile(context.Background(), "abc123", 1<<20)
	if err != nil {
		t.Fatalf("FetchFile: %v", err)
	}
	if string(data) != want {
		t.Errorf("data = %q, want %q", data, want)
	}
	if fpath != "photos/file_42.jpg" {
		t.Errorf("path = %q", fpath)
	}
	if getFilePath != "/bottok/getFile" {
		t.Errorf("getFile path = %q", getFilePath)
	}
	// The download endpoint differs from the API method endpoint.
	if downloadPath != "/file/bottok/photos/file_42.jpg" {
		t.Errorf("download path = %q", downloadPath)
	}
}

func TestFetchFileTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"result":{"file_path":"x.jpg","file_size":5000}}`)
	}))
	defer srv.Close()

	c := New("tok")
	c.base = srv.URL
	if _, _, err := c.FetchFile(context.Background(), "abc", 1000); err == nil {
		t.Fatal("want error when getFile reports a size over the limit")
	}
}

func TestGetFileError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":false,"description":"file not found"}`)
	}))
	defer srv.Close()

	c := New("tok")
	c.base = srv.URL
	_, err := c.GetFile(context.Background(), "abc")
	if err == nil || !strings.Contains(err.Error(), "file not found") {
		t.Fatalf("want API description in error, got %v", err)
	}
}
