package telemetry

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The catalogue is the contract. Every event matterbox can send, and every
// property that event can carry, is declared in catalogue_events.go — and
// Capture drops anything that isn't. That inverts the usual arrangement, where
// an event is whatever a call site happened to pass and the documentation is a
// best-effort description written afterwards: here the declaration is checked
// at runtime, `docs/telemetry.md` is generated from it, and tests fail if a
// call site emits something undeclared or if the generated doc has drifted.
//
// Three things follow, which is the whole reason for the machinery:
//
//   - A property can only hold a value its PropSpec allows. An enum property
//     accepts nothing outside Values; a count accepts a non-negative integer.
//     So a call site that accidentally passes a message body, a username or a
//     channel name does not leak it — the value fails validation and is
//     dropped before the event is queued.
//   - Nothing ships undocumented. `docs/telemetry.md` is rendered from these
//     specs, so the published list of what matterbox sends cannot be stale or
//     incomplete: a new event is either in the catalogue and therefore in the
//     docs, or it never reaches PostHog at all.
//   - The reverse also holds. The catalogue records, per event, the product
//     question it exists to answer (EventSpec.Why). An event nobody can name a
//     use for is an event to delete, and reviewing the doc makes that obvious.

// Kind is the type of a property's value, and therefore what validate will let
// through. Kinds are coarse on purpose: the narrower the set of shapes a
// property can hold, the less room there is for something personal to ride
// along in one.
type Kind int

const (
	// KindEnum is one label from a closed set. The workhorse: every bucketed
	// number and every categorical dimension is an enum, so the set of values
	// a property can ever hold is finite, declared, and documented.
	KindEnum Kind = iota
	// KindEnumSet is a list of labels from a closed set, for "which of these
	// applied" properties (enabled features, overridden keybindings). PostHog
	// breaks arrays down element-wise, so this stays queryable.
	KindEnumSet
	// KindBool is a flag.
	KindBool
	// KindCount is an exact non-negative integer, for counts of things that
	// are ours rather than the user's: rules loaded, panes open, retries. Never
	// use it for anything derived from user content — that is what Count and
	// the other bucket helpers are for.
	KindCount
	// KindCounterMap is a map from a whitelisted key to an exact count, the
	// shape the usage snapshot uses to report "this action fired 14 times".
	// Keys outside Values are dropped, which is what keeps the snapshot from
	// becoming an open-ended bag of strings.
	KindCounterMap
	// KindVersion is a build identifier — a version, a tag list, a Go version.
	// Free-form but charset-restricted (see reVersion): these describe the
	// binary, not the person running it.
	KindVersion
	// KindFrames is a list of matterbox stack frames, as ScrubStack produces
	// them. Open-ended rather than a closed set — the function names are
	// whatever our own code is called — but every element must match reFrame,
	// so only "internal/pkg.Func"-shaped strings from our own module get
	// through and a path or an argument cannot.
	KindFrames
	// KindErrorText is scrubbed error text. The only property kind that carries
	// prose, and every value is put through Scrub first. Used sparingly, and
	// never on an event that also identifies what the user was doing in detail.
	KindErrorText
)

// String names the kind for the generated documentation.
func (k Kind) String() string {
	switch k {
	case KindEnum:
		return "enum"
	case KindEnumSet:
		return "enum list"
	case KindBool:
		return "bool"
	case KindCount:
		return "count"
	case KindCounterMap:
		return "counter map"
	case KindFrames:
		return "stack frames"
	case KindVersion:
		return "build string"
	case KindErrorText:
		return "scrubbed text"
	}
	return "unknown"
}

// reVersion bounds what a KindVersion property may contain: the characters a
// version, git describe output, tag list or platform string needs, and nothing
// that could carry a path or a sentence.
var reVersion = regexp.MustCompile(`^[\w.+\-/, ()]{1,120}$`)

// reFrame bounds a KindFrames element: a package path inside the matterbox
// module plus a function name, exactly the shape ScrubStack emits. Anything
// carrying a slash-prefixed absolute path, a space or an argument list fails.
var reFrame = regexp.MustCompile(`^(internal/[\w./]+|main)\.[\w.*()]+$`)

// maxFramesSent caps a KindFrames list. ScrubStack already truncates; this is
// the validation-side guarantee so the cap does not depend on the caller.
const maxFramesSent = 12

// PropSpec declares one property of one event.
type PropSpec struct {
	// Name is the PostHog property name. Snake case, stable — renaming one
	// orphans the insights built on it, so treat a name as published.
	Name string
	Kind Kind
	// Values is the closed set for KindEnum and KindEnumSet, and the allowed
	// key set for KindCounterMap. Ignored by the other kinds.
	Values []string
	// Desc is the one-line explanation that lands in docs/telemetry.md. Write
	// it for someone deciding whether to opt in, not for us.
	Desc string
}

// EventSpec declares one event.
type EventSpec struct {
	// Name is the PostHog event name. Snake case, past tense for things that
	// happened ("message_sent"), stable once shipped.
	Name string
	// Desc says when the event fires.
	Desc string
	// Why names the product question the event exists to answer. An event
	// without one is noise; requiring it here is how the catalogue stays a
	// deliberate list rather than an accumulation.
	Why string
	// Emitter is the exported function in emit.go that sends this event. Named
	// here so TestEveryCataloguedEventIsEmitted can check the app actually calls
	// it: an emitter that exists but is never called would put an event in the
	// published docs that no user ever sends, which is worse than omitting it.
	Emitter string
	// Trigger names the exported call the app makes when it is not the emitter
	// itself, so the completeness check above looks for the right thing. Set it
	// only where the indirection is deliberate: panic reporting has to take the
	// panic value from recover() and the stack while it is still standing, so
	// call sites use Crash and the emitter is reached from inside this package.
	Trigger string
	Props   []PropSpec
	// Planned marks an event that is declared and documented but not yet
	// emitted anywhere. It keeps the catalogue usable as the design of the
	// telemetry rather than only its current state, and the completeness test
	// exempts these from needing a call site — while still requiring that
	// anything actually emitted be declared.
	Planned bool
	// Daemon marks an event that only `matterbox listen` sends. Called out in
	// the docs because the daemon runs unattended on a server, where "what is
	// this process sending" deserves its own answer.
	Daemon bool
}

// eventIndex indexes the catalogue by name for Capture's validation, and is
// built once at init. A duplicate name is a programming error that would make
// one of the two events silently unreachable, so it panics: this runs at
// process start in every build, including tests, so it cannot ship.
var eventIndex = func() map[string]*EventSpec {
	m := make(map[string]*EventSpec, len(Events))
	for i := range Events {
		e := &Events[i]
		if _, dup := m[e.Name]; dup {
			panic("telemetry: duplicate event in catalogue: " + e.Name)
		}
		m[e.Name] = e
	}
	return m
}()

// Spec returns the catalogue entry for an event name.
func Spec(event string) (*EventSpec, bool) {
	e, ok := eventIndex[event]
	return e, ok
}

// prop finds a property spec on an event.
func (e *EventSpec) prop(name string) (PropSpec, bool) {
	for _, p := range e.Props {
		if p.Name == name {
			return p, true
		}
	}
	return PropSpec{}, false
}

// sanitize filters props down to what the event's spec allows, returning the
// cleaned map. Anything undeclared or invalid is dropped rather than the event
// being rejected: a call site with one bad property still carries useful
// information in its good ones, and losing an event outright would be a worse
// failure than losing a field of it.
//
// dropped names what was discarded, which the strict-mode tests assert is
// empty — so a mistake is loud in CI and quiet in production, which is the
// right way round.
func (e *EventSpec) sanitize(props map[string]any) (clean map[string]any, dropped []string) {
	if len(props) == 0 {
		return nil, nil
	}
	clean = make(map[string]any, len(props))
	for name, v := range props {
		spec, ok := e.prop(name)
		if !ok {
			dropped = append(dropped, name)
			continue
		}
		cv, ok := validate(spec, v)
		if !ok {
			dropped = append(dropped, name)
			continue
		}
		clean[name] = cv
	}
	sort.Strings(dropped)
	return clean, dropped
}

// validate checks one value against its spec and returns the value to send.
// The returned value may differ from the input: error text comes back scrubbed,
// and a counter map comes back with its disallowed keys removed.
func validate(spec PropSpec, v any) (any, bool) {
	switch spec.Kind {
	case KindEnum:
		s, ok := v.(string)
		if !ok || !inSet(spec.Values, s) {
			return nil, false
		}
		return s, true

	case KindEnumSet:
		items, ok := v.([]string)
		if !ok {
			return nil, false
		}
		// Filter rather than reject: a caller reporting five enabled features
		// where one name is stale should still tell us about the other four.
		out := make([]string, 0, len(items))
		for _, s := range items {
			if inSet(spec.Values, s) {
				out = append(out, s)
			}
		}
		sort.Strings(out)
		return out, true

	case KindBool:
		b, ok := v.(bool)
		return b, ok

	case KindCount:
		n, ok := asInt(v)
		if !ok || n < 0 {
			return nil, false
		}
		return n, true

	case KindCounterMap:
		counts, ok := v.(map[string]int)
		if !ok {
			return nil, false
		}
		out := make(map[string]int, len(counts))
		for k, n := range counts {
			if n <= 0 || !inSet(spec.Values, k) {
				continue
			}
			out[k] = n
		}
		if len(out) == 0 {
			// An empty map is noise in the property list; drop the property and
			// let its absence mean "nothing happened".
			return nil, false
		}
		return out, true

	case KindFrames:
		items, ok := v.([]string)
		if !ok {
			return nil, false
		}
		out := make([]string, 0, len(items))
		for _, f := range items {
			if reFrame.MatchString(f) {
				out = append(out, f)
			}
			if len(out) == maxFramesSent {
				break
			}
		}
		if len(out) == 0 {
			return nil, false
		}
		return out, true

	case KindVersion:
		s, ok := v.(string)
		if !ok || !reVersion.MatchString(s) {
			return nil, false
		}
		return s, true

	case KindErrorText:
		s, ok := v.(string)
		if !ok {
			if err, isErr := v.(error); isErr {
				s = err.Error()
			} else {
				return nil, false
			}
		}
		s = Scrub(s)
		if s == "" {
			return nil, false
		}
		return s, true
	}
	return nil, false
}

// asInt accepts the integer types a call site might plausibly hold, so a
// caller with an int64 duration or a uint count doesn't have to remember to
// convert. Floats are rejected: a fractional count is a sign the caller meant
// something else.
func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case uint:
		return int(n), true
	case uint32:
		return int(n), true
	case uint64:
		return int(n), true
	}
	return 0, false
}

func inSet(set []string, s string) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}

// Validate checks the catalogue itself for the mistakes that would make it a
// worse contract than no contract: an event with no stated purpose, an enum
// with nothing to compare against, a name that doesn't match the convention
// the dashboards assume. Called by a test, so a malformed entry fails CI
// rather than shipping a property nobody can query.
func Validate() error {
	var errs []string
	name := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	for _, e := range Events {
		where := "event " + e.Name
		if !name.MatchString(e.Name) {
			errs = append(errs, where+": name must be lower snake case")
		}
		if strings.TrimSpace(e.Desc) == "" {
			errs = append(errs, where+": missing Desc")
		}
		if strings.TrimSpace(e.Why) == "" {
			errs = append(errs, where+": missing Why — an event nobody can name a use for should not exist")
		}
		if strings.TrimSpace(e.Emitter) == "" {
			errs = append(errs, where+": missing Emitter")
		}
		seen := map[string]bool{}
		for _, p := range e.Props {
			pw := where + ", prop " + p.Name
			if !name.MatchString(p.Name) {
				errs = append(errs, pw+": name must be lower snake case")
			}
			if seen[p.Name] {
				errs = append(errs, pw+": duplicate property")
			}
			seen[p.Name] = true
			if strings.TrimSpace(p.Desc) == "" {
				errs = append(errs, pw+": missing Desc")
			}
			needsValues := p.Kind == KindEnum || p.Kind == KindEnumSet || p.Kind == KindCounterMap
			if needsValues && len(p.Values) == 0 {
				errs = append(errs, pw+": "+p.Kind.String()+" needs Values")
			}
			if !needsValues && len(p.Values) > 0 {
				errs = append(errs, pw+": "+p.Kind.String()+" must not declare Values")
			}
			for _, v := range p.Values {
				if strings.TrimSpace(v) == "" {
					errs = append(errs, pw+": empty value in Values")
				}
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("telemetry catalogue:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

// EventNames lists every catalogued event name, sorted. Used by the
// completeness test and by the docs generator.
func EventNames() []string {
	out := make([]string, 0, len(Events))
	for _, e := range Events {
		out = append(out, e.Name)
	}
	sort.Strings(out)
	return out
}
