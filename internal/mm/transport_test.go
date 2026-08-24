package mm

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"
)

// stubRT answers with errs[i] for call i, and 200 once the list runs out.
type stubRT struct {
	errs  []error
	calls []*http.Request
}

func (s *stubRT) RoundTrip(req *http.Request) (*http.Response, error) {
	s.calls = append(s.calls, req)
	if n := len(s.calls) - 1; n < len(s.errs) && s.errs[n] != nil {
		return nil, s.errs[n]
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Request: req}, nil
}

func TestRetryOnce(t *testing.T) {
	reset := &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
	for _, tc := range []struct {
		name      string
		method    string
		body      io.Reader
		errs      []error
		wantCalls int
		wantErr   bool
	}{
		{"reset get is replayed", http.MethodGet, nil, []error{reset}, 2, false},
		{"eof get is replayed", http.MethodGet, nil, []error{io.EOF}, 2, false},
		{"still dead after replay", http.MethodGet, nil, []error{reset, reset}, 2, true},
		{"post is not replayed", http.MethodPost, strings.NewReader("{}"), []error{reset}, 1, true},
		{"refused is not a mid-flight death", http.MethodGet, nil,
			[]error{&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}}, 1, true},
		{"success is untouched", http.MethodGet, nil, nil, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := &stubRT{errs: tc.errs}
			req, err := http.NewRequestWithContext(context.Background(), tc.method, "http://x/api/v4/posts", tc.body)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			resp, err := (retryOnce{base: base}).RoundTrip(req)
			if resp != nil {
				resp.Body.Close()
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if len(base.calls) != tc.wantCalls {
				t.Errorf("calls = %d, want %d", len(base.calls), tc.wantCalls)
			}
		})
	}
}

// A cancelled request is not worth replaying — nobody is waiting for it.
func TestRetryOnceHonoursContext(t *testing.T) {
	base := &stubRT{errs: []error{syscall.ECONNRESET}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://x/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, err := (retryOnce{base: base, delay: time.Minute}).RoundTrip(req); err == nil {
		t.Fatal("want the connection error back")
	}
	if len(base.calls) != 1 {
		t.Errorf("calls = %d, want 1 (no replay after cancel)", len(base.calls))
	}
}

// End to end through the real client: the first fetch dies on a connection the
// server kills without answering, and Posts still returns the page.
func TestPostsSurvivesAConnectionReset(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			if tcp, ok := conn.(*net.TCPConn); ok {
				_ = tcp.SetLinger(0) // close with an RST, not a polite FIN
			}
			conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"order":["p1"],"posts":{"p1":{"id":"p1","message":"hi"}}}`))
	}))
	defer srv.Close()

	pl, err := New(srv.URL, "tok").Posts(context.Background(), "chan", 30)
	if err != nil {
		t.Fatalf("posts: %v", err)
	}
	if len(pl.Order) != 1 || pl.Posts["p1"] == nil {
		t.Errorf("post list = %+v, want the single post", pl)
	}
	if hits != 2 {
		t.Errorf("server saw %d requests, want 2 (the death and the replay)", hits)
	}
}

func TestConnectionDiedIgnoresOrdinaryFailures(t *testing.T) {
	for _, err := range []error{
		errors.New("500 internal server error"),
		context.DeadlineExceeded,
		&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED},
	} {
		if connectionDied(err) {
			t.Errorf("connectionDied(%v) = true, want false", err)
		}
	}
}
