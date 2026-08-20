package githubauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStartDeviceFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/login/device/code" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("client_id") != "cid" {
			t.Errorf("client_id = %q", r.Form.Get("client_id"))
		}
		json.NewEncoder(w).Encode(map[string]any{
			"device_code":      "dev123",
			"user_code":        "ABCD-1234",
			"verification_uri": "https://github.com/login/device",
			"expires_in":       900,
			"interval":         5,
		})
	}))
	defer srv.Close()

	start, err := StartDeviceFlow(context.Background(), srv.Client(), srv.URL, "cid", []string{"repo"})
	if err != nil {
		t.Fatal(err)
	}
	if start.DeviceCode != "dev123" || start.UserCode != "ABCD-1234" {
		t.Errorf("start = %+v", start)
	}
	if start.Interval != 5*time.Second {
		t.Errorf("interval = %v", start.Interval)
	}
}

func TestPollDeviceFlowSuccess(t *testing.T) {
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/login/oauth/access_token" {
			t.Errorf("path = %s", r.URL.Path)
		}
		n := polls.Add(1)
		if n < 2 {
			json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"access_token": "gho_ok"})
	}))
	defer srv.Close()

	start := DeviceStart{
		DeviceCode: "dev",
		ExpiresAt:  time.Now().Add(time.Minute),
		Interval:   10 * time.Millisecond,
	}
	tok, err := PollDeviceFlow(context.Background(), srv.Client(), srv.URL, "cid", start)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "gho_ok" {
		t.Errorf("token = %q", tok)
	}
	if polls.Load() < 2 {
		t.Errorf("expected at least 2 polls, got %d", polls.Load())
	}
}

func TestVerifyAccessToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("missing Bearer header")
		}
		if r.URL.Path != "/user" {
			t.Errorf("path = %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]string{"login": "octocat"})
	}))
	defer srv.Close()

	login, err := VerifyAccessToken(context.Background(), srv.Client(), srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if login != "octocat" {
		t.Errorf("login = %q", login)
	}
}
