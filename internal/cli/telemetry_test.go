package cli

import (
	"errors"
	"testing"

	"matterbox/internal/telemetry"
)

// TestCLICommandsMatchRegistry: every registered subcommand must be in the
// catalogue, or reportCommand stays silent about it and the verb looks unused
// forever — which is exactly the conclusion the event exists to prevent.
func TestCLICommandsMatchRegistry(t *testing.T) {
	root := newRootCmd()
	for _, cmd := range root.Commands() {
		if !telemetry.KnownCLICommand(cmd.Name()) {
			t.Errorf("subcommand %q is not in telemetry.CLICommands — add it there "+
				"and run `go generate ./internal/telemetry`", cmd.Name())
		}
	}
}

// TestClassifyGroupsByWhatToDo pins the error mapping to what a user would have
// to do about the failure, which is the only grouping that helps prioritise.
func TestClassifyGroupsByWhatToDo(t *testing.T) {
	cases := []struct {
		err            error
		outcome, class string
	}{
		{nil, "ok", ""},
		{errors.New("no saved login; run `matterbox login`"), "error", "auth"},
		{errors.New(`Post "https://chat.example.com/api/v4/posts": dial tcp: no such host`), "error", "network"},
		{errors.New("unknown flag: --nope"), "error", "config"},
		{errors.New("channel not found"), "error", "not_found"},
		{errors.New("429 rate limit exceeded"), "error", "rate_limited"},
		{errors.New("something nobody anticipated"), "error", "unknown"},
	}
	for _, c := range cases {
		outcome, class := classify(c.err)
		if outcome != c.outcome || class != c.class {
			t.Errorf("classify(%v) = (%q, %q), want (%q, %q)",
				c.err, outcome, class, c.outcome, c.class)
		}
	}
}

// TestOutcomesAndClassesAreCatalogued: classify's return values feed enum
// properties, so anything it can produce has to be a declared value or the
// property is dropped and the failure becomes invisible.
func TestOutcomesAndClassesAreCatalogued(t *testing.T) {
	outcomes := map[string]bool{}
	for _, o := range telemetry.Outcomes {
		outcomes[o] = true
	}
	classes := map[string]bool{}
	for _, c := range telemetry.ErrorClasses {
		classes[c] = true
	}
	// Every branch classify can take, exercised through representative errors.
	for _, err := range []error{
		nil,
		errors.New("unauthorized"), errors.New("forbidden"), errors.New("404"),
		errors.New("429"), errors.New("dial tcp"), errors.New("500"),
		errors.New("yaml: line 3"), errors.New("no space left"),
		errors.New("unknown flag"), errors.New("mystery"),
	} {
		outcome, class := classify(err)
		if !outcomes[outcome] {
			t.Errorf("classify(%v) produced outcome %q, which is not catalogued", err, outcome)
		}
		if class != "" && !classes[class] {
			t.Errorf("classify(%v) produced class %q, which is not catalogued", err, class)
		}
	}
}
