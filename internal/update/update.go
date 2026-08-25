// Package update answers one question: is there a newer matterbox than the one
// running?
//
// It exists because an installed copy is otherwise the last to know. A release
// is announced on a page nobody has a reason to revisit, so the only people who
// upgrade are the ones who happened to look — which means "this bug is fixed"
// and "nobody is running the fix" stay indistinguishable for months.
//
// What it does not do is upgrade anything. The check reports; `matterbox
// upgrade` acts, and only when asked. A terminal client that swapped its own
// binary out from under a running session would be trading a great deal of
// trust for a few saved keystrokes.
//
// # The request
//
// One GET of a small JSON document, at most once a day, to a URL that answers
// everyone the same way. It carries no version, no platform and no identifier:
// the comparison happens here, on the client, precisely so the server has
// nothing to compare. That is what keeps this out of the opt-in telemetry
// consent — it is a request for a file, no more revealing than loading the
// website, and gating it behind that consent would mean the people most careful
// about their privacy are the ones who never hear about a fix.
//
// Which versions people actually run is a real question, and it has a real
// answer elsewhere: the version_upgraded event, which asks first. See
// internal/telemetry.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"matterbox/internal/config"
)

// Endpoint is served by a Worker on matterbox.work, which reads GitHub's own
// "latest release" redirect and caches it. Deliberately our own domain and not
// github.com directly: this string is compiled into every release and can never
// be changed for the copies already installed, so it has to be one we can still
// decide the meaning of — to add a field, answer from somewhere else, or
// withhold a release found to be bad.
//
// A var rather than a const only so a test can point it at a local server. It
// is not a knob: nothing reads it from the config or the environment.
var Endpoint = "https://matterbox.work/latest.json"

const (
	// Interval is how long a successful answer is trusted. A day is the right
	// order of magnitude for something that changes every few weeks: it costs
	// one request, and nobody needs to hear about a release within the hour.
	Interval = 24 * time.Hour
	// retryInterval is the wait after a failed check. Shorter than Interval so
	// one flaky moment does not cost a day of silence, long enough that a
	// laptop with no network does not ask on every launch.
	retryInterval = time.Hour
	// timeout bounds the whole request. Nothing waits on this — it runs beside
	// a UI that is already up — but an answer that arrives after the session
	// has ended is of no use to anyone.
	timeout = 5 * time.Second
	// stateFile remembers the last answer and when it was given, inside the
	// matterbox directory. Deliberately not config.yaml: that file is the
	// user's document, hand-edited and commented, and rewriting it wholesale to
	// record a timestamp would be a poor trade.
	stateFile = "update.json"
)

// Release is what the endpoint says the current release is. Fields we do not
// recognise are ignored, which is what lets the endpoint grow one without
// breaking a matterbox that shipped before it existed.
type Release struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	Install string `json:"install"`
}

// state is stateFile: the last answer, and when it was last asked for. Checked
// is written on failure too, so an unreachable endpoint is not asked again on
// every launch.
type state struct {
	Checked time.Time `json:"checked"`
	Failed  bool      `json:"failed,omitempty"`
	Release *Release  `json:"release,omitempty"`
}

// Check reports the current release when it is newer than current, and nil when
// it is not — including every case where the question cannot be answered: the
// check is disabled, the network is down, the endpoint is unhappy, or this
// build carries no version to compare against.
//
// An error is returned alongside a nil release only for the caller that wants
// to say why (`matterbox upgrade --check`). The TUI ignores it: a failed update
// check is not something to interrupt anyone about.
func Check(ctx context.Context, current string) (*Release, error) {
	// Whether the check may run at all is the caller's question — it is a
	// config switch, and the UI has already read the config (see
	// config.UpdateCheckEnabled).
	//
	// A build with no release name — `go build` from a working tree, or an
	// install from a branch — has nothing to compare, and telling its owner
	// they are "behind v1.2.0" would be wrong as often as right.
	if _, ok := triple(current); !ok {
		return nil, nil
	}

	st := readState()
	if st.fresh() {
		return newer(current, st.Release), nil
	}

	rel, err := fetch(ctx)
	writeState(state{Checked: time.Now(), Failed: err != nil, Release: orKeep(rel, st.Release)})
	if err != nil {
		// The previous answer is still a fact about the world, so it is still
		// worth comparing against — it is only the freshness that expired.
		return newer(current, st.Release), err
	}
	return newer(current, rel), nil
}

// Force is Check with the interval ignored and the config's off switch
// overridden: what `matterbox upgrade` does, because a person who typed the
// command is asking now and about this machine, whatever the automatic check is
// configured to do.
func Force(ctx context.Context) (*Release, error) {
	rel, err := fetch(ctx)
	if err != nil {
		return nil, err
	}
	writeState(state{Checked: time.Now(), Release: rel})
	return rel, nil
}

func (s state) fresh() bool {
	if s.Checked.IsZero() {
		return false
	}
	wait := Interval
	if s.Failed {
		wait = retryInterval
	}
	return time.Since(s.Checked) < wait
}

// newer is the comparison every path funnels through, so "is there an update"
// has exactly one definition.
func newer(current string, rel *Release) *Release {
	if rel == nil || !Newer(current, rel.Version) {
		return nil
	}
	return rel
}

// orKeep prefers a fresh answer but does not throw away the last good one when
// the request failed.
func orKeep(fresh, previous *Release) *Release {
	if fresh != nil {
		return fresh
	}
	return previous
}

func fetch(ctx context.Context) (*Release, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Endpoint, nil)
	if err != nil {
		return nil, err
	}
	// A bare product name and nothing else. No version, no platform, no id: the
	// point of the exercise is that the endpoint cannot tell one asker from
	// another, and a User-Agent is the easiest place to lose that by accident.
	req.Header.Set("User-Agent", "matterbox")
	req.Header.Set("Accept", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", Endpoint, res.Status)
	}
	// Bounded: this is a handful of fields, and a body that is not cannot be
	// one of ours.
	var rel Release
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("%s: %w", Endpoint, err)
	}
	if _, ok := triple(rel.Version); !ok {
		return nil, fmt.Errorf("%s: %q is not a version", Endpoint, rel.Version)
	}
	return &rel, nil
}

// statePath is stateFile inside the matterbox directory, or "" when there is no
// such directory to speak of — in which case the check still works, it just
// asks every launch.
func statePath() string {
	p, err := config.File(stateFile)
	if err != nil {
		return ""
	}
	return p
}

func readState() state {
	var s state
	p := statePath()
	if p == "" {
		return s
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return s
	}
	if err := json.Unmarshal(data, &s); err != nil {
		// A corrupt file is not worth a word to anyone: the next check
		// overwrites it, and the cost of ignoring it is one extra request.
		return state{}
	}
	return s
}

// writeState is best-effort throughout. Failing to remember when we last asked
// means asking again sooner than intended, which is not worth an error anybody
// has to handle.
func writeState(s state) {
	p := statePath()
	if p == "" {
		return
	}
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(p, append(data, '\n'), 0o600)
}

// pending is the answer the TUI found, kept for the command layer to print
// after the program has released the terminal. Two surfaces, one fact: the
// toast in the TUI is easy to miss, and the line on exit lands where the
// command can actually be typed.
var pending struct {
	mu  sync.Mutex
	rel *Release
}

// SetPending records a newer release for Pending to hand back. Called by the UI
// when its check comes back positive.
func SetPending(rel *Release) {
	pending.mu.Lock()
	defer pending.mu.Unlock()
	pending.rel = rel
}

// Pending is the newer release this process found, or nil. Read by internal/cli
// once the TUI has exited.
func Pending() *Release {
	pending.mu.Lock()
	defer pending.mu.Unlock()
	return pending.rel
}
