package telemetry

import (
	"context"
	"errors"
	"os"
	"strings"
)

// Classify maps an error onto the catalogue's outcome and error class. Grouping
// by what a user would have to do about it — check the network, log in again,
// fix the config — is the only grouping that helps decide what to fix, and it
// is what decides whether a failure also becomes an error-tracking issue (see
// worthAnIssue): the classes below that mean "the world" never do.
//
// Matching on message text is crude, but these errors come from several layers
// (cobra, net/http, the Mattermost client, os) with no shared error types to
// switch on, and the alternative is a single "error" bucket that says nothing.
func Classify(err error) (outcome, class string) {
	if err == nil {
		return "ok", ""
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", "network"
	case errors.Is(err, context.Canceled):
		return "cancelled", ""
	case errors.Is(err, os.ErrPermission):
		return "error", "permission"
	case errors.Is(err, os.ErrNotExist):
		return "error", "not_found"
	}
	msg := strings.ToLower(err.Error())
	contains := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(msg, s) {
				return true
			}
		}
		return false
	}
	switch {
	// Cobra's own complaints: a usage mistake, not a failure of the app.
	case contains("unknown flag", "unknown command", "unknown shorthand",
		"accepts ", "requires at least", "invalid argument"):
		return "error", "config"
	case contains("no saved login", "not logged in", "unauthorized", "401",
		"invalid or expired session", "token"):
		return "error", "auth"
	case contains("403", "forbidden", "permission"):
		return "error", "permission"
	case contains("404", "not found", "no such channel", "no such user"):
		return "error", "not_found"
	case contains("429", "rate limit"):
		return "error", "rate_limited"
	case contains("no such host", "connection refused", "dial tcp", "eof",
		"network is unreachable", "tls", "timeout", "deadline exceeded"):
		return "error", "network"
	case contains("500", "502", "503", "504", "internal server error"):
		return "error", "server"
	case contains("yaml", "parse", "unmarshal", "invalid character"):
		return "error", "parse"
	case contains("no space left", "read-only file system", "disk"):
		return "error", "disk"
	}
	return "error", "unknown"
}

// ClassifyError is Classify when only the class is wanted, which is the common
// case at a failure site that already knows it failed.
func ClassifyError(err error) string {
	_, class := Classify(err)
	if class == "" {
		return "unknown"
	}
	return class
}
