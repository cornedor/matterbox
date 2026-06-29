package mm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// loginServer fakes Mattermost's /api/v4/users/login: it accepts password
// "hunter2", issues wantToken on success, and forces MFA for the "mfauser"
// account until a token is supplied.
func loginServer(t *testing.T, wantToken string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/users/login" || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeErr := func(id string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte((&model.AppError{Id: id, Message: id, StatusCode: http.StatusUnauthorized}).ToJSON()))
		}
		switch {
		case body["login_id"] == "mfauser" && body["token"] == "":
			writeErr("mfa.validate_token.authenticate.app_error")
		case body["password"] != "hunter2":
			writeErr("api.user.login.invalid_credentials_email_username.app_error")
		default:
			w.Header().Set(model.HeaderToken, wantToken)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(&model.User{Id: "uid", Username: body["login_id"], Email: body["login_id"] + "@example.com"})
		}
	}))
}

func TestLoginWithPassword(t *testing.T) {
	const wantToken = "sesstoken123"
	srv := loginServer(t, wantToken)
	defer srv.Close()
	ctx := context.Background()

	t.Run("happy path", func(t *testing.T) {
		tok, user, err := New(srv.URL, "").LoginWithPassword(ctx, "alice", "hunter2", "")
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		if tok != wantToken {
			t.Errorf("token = %q, want %q", tok, wantToken)
		}
		if user.Username != "alice" {
			t.Errorf("username = %q, want alice", user.Username)
		}
	})

	t.Run("bad password", func(t *testing.T) {
		_, _, err := New(srv.URL, "").LoginWithPassword(ctx, "alice", "wrong", "")
		if err == nil {
			t.Fatal("expected an error for a bad password")
		}
		if MFARequired(err) {
			t.Errorf("a bad-credentials error must not read as MFA-required: %v", err)
		}
	})

	t.Run("mfa: signalled then accepted with a token", func(t *testing.T) {
		c := New(srv.URL, "")
		if _, _, err := c.LoginWithPassword(ctx, "mfauser", "hunter2", ""); !MFARequired(err) {
			t.Fatalf("first attempt: want MFARequired, got %v", err)
		}
		tok, _, err := c.LoginWithPassword(ctx, "mfauser", "hunter2", "123456")
		if err != nil {
			t.Fatalf("retry with token: %v", err)
		}
		if tok != wantToken {
			t.Errorf("token = %q, want %q", tok, wantToken)
		}
	})
}

func TestMFARequired(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"generic", errors.New("invalid credentials"), false},
		{"app error id", &model.AppError{Id: "mfa.validate_token.authenticate.app_error"}, true},
		{"wrapped app error id", fmt.Errorf("login: %w", &model.AppError{Id: "api.context.mfa_required.app_error"}), true},
		{"text mentions mfa", errors.New("login: please supply your MFA token"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MFARequired(c.err); got != c.want {
				t.Errorf("MFARequired(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
