package mm

import (
	"errors"
	"io"
	"net/http"
	"syscall"
	"time"
)

// retryDelay is the pause before the single replay. Long enough that a server
// closing a batch of connections has finished doing so, short enough that the
// user never notices the fetch took two round trips.
const retryDelay = 150 * time.Millisecond

// retryOnce replays a read-only API request whose connection died mid-flight.
//
// Keep-alive connections are closed by the server (or the proxy in front of it)
// on its own schedule, and a request that goes out on one just as it goes away
// comes back as "read tcp …: connection reset by peer". net/http replays that
// itself only for HTTP/1 requests on a connection it knows was reused; over
// HTTP/2 — what a Mattermost server behind TLS negotiates — nothing is replayed
// and the error surfaces. To the UI that is indistinguishable from a real
// failure: a red status line and a reported failure for a blip a second attempt
// almost always survives.
//
// Only bodyless GET/HEAD is replayed. A send must never be duplicated, and its
// body has been consumed anyway.
type retryOnce struct {
	base  http.RoundTripper
	delay time.Duration
}

func (t retryOnce) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if err == nil || !replayable(req) || !connectionDied(err) {
		return resp, err
	}
	select {
	case <-req.Context().Done():
		return resp, err
	case <-time.After(t.delay):
	}
	return base.RoundTrip(req)
}

// replayable reports whether re-sending the request is safe. A body rules it
// out even for a GET: the reader is already drained.
func replayable(req *http.Request) bool {
	switch req.Method {
	case http.MethodGet, http.MethodHead:
	default:
		return false
	}
	return req.Body == nil || req.Body == http.NoBody
}

// connectionDied reports whether err means the connection went away rather than
// the request being answered badly. A truncated response is included: the
// server never said what it thought, so we learned nothing by asking once.
func connectionDied(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.EOF)
}
