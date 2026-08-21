package ui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"matterbox/internal/config"
	"matterbox/internal/telemetry"
)

// TestActionIDsMatchKeymap is the parity check that lets the telemetry package
// hold a copy of the action list. telemetry.ActionIDs is the whitelist the
// usage snapshot's counter map is validated against and the list published in
// docs/telemetry.md, so an action added to the registry without being added
// there would be silently uncounted — the one failure mode that looks exactly
// like "nobody uses this feature".
func TestActionIDsMatchKeymap(t *testing.T) {
	registry := make([]string, 0, len(actionDefs))
	for _, d := range actionDefs {
		registry = append(registry, d.id)
	}
	sort.Strings(registry)

	catalogued := append([]string(nil), telemetry.ActionIDs...)
	sort.Strings(catalogued)

	if strings.Join(registry, ",") != strings.Join(catalogued, ",") {
		t.Errorf("telemetry.ActionIDs is out of step with actionDefs.\n"+
			"missing from telemetry.ActionIDs: %v\n"+
			"in telemetry.ActionIDs but not the registry: %v\n"+
			"After fixing, run `go generate ./internal/telemetry`.",
			missing(registry, catalogued), missing(catalogued, registry))
	}
}

// TestContextNamesAreCatalogued: the keyContexts ladder supplies the `surface`
// dimension on every keyboard event, so a layer missing from
// telemetry.Contexts would have its events silently stripped of the one
// property that makes them interpretable.
func TestContextNamesAreCatalogued(t *testing.T) {
	catalogued := make(map[string]bool, len(telemetry.Contexts))
	for _, c := range telemetry.Contexts {
		catalogued[c] = true
	}
	for _, c := range keyContexts {
		if !catalogued[c.name] {
			t.Errorf("key context %q is not in telemetry.Contexts — add it there and "+
				"run `go generate ./internal/telemetry`", c.name)
		}
	}
}

// TestActionFingerprintsAreUnique guards the attribution lookup: two actions
// sharing both a description and a key list would be indistinguishable, and one
// of them would be credited with the other's presses. Several actions do share
// a description ("up" is both `up` and `input_up`), so the key list is what
// separates them.
func TestActionFingerprintsAreUnique(t *testing.T) {
	for _, nav := range []string{"ctrl", "none"} {
		km := newKeyMap(nav)
		seen := make(map[string]string, len(actionDefs))
		for _, d := range actionDefs {
			fp := bindingFingerprint(*d.field(&km))
			if other, dup := seen[fp]; dup {
				t.Errorf("nav=%s: actions %q and %q have identical fingerprints "+
					"(same description and keys), so presses cannot be attributed",
					nav, other, d.id)
			}
			seen[fp] = d.id
		}
		if len(km.actionIDs) != len(actionDefs) {
			t.Errorf("nav=%s: actionIDs holds %d entries for %d actions",
				nav, len(km.actionIDs), len(actionDefs))
		}
	}
}

// TestOverridesRebuildIndexes: a rebound key must be attributed to its action
// under the new binding, and the old key must stop counting as bound.
func TestOverridesRebuildIndexes(t *testing.T) {
	km, err := applyKeyOverrides(newKeyMap("ctrl"), map[string]config.StringOrList{
		"react": {"ctrl+alt+r"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := km.actionID(km.React); got != "react" {
		t.Errorf("rebound action resolved to %q, want \"react\"", got)
	}
	if !km.boundSomewhere("ctrl+alt+r") {
		t.Error("the override's key is not reported as bound")
	}
	if km.boundSomewhere("R") && len(actionsForKey(km, "R")) == 0 {
		t.Error("the replaced default key is still reported as bound")
	}
}

// TestRecordKeyAttributesAndReportsUnhandled is the end-to-end check on the
// hook: with telemetry running, a bound key in a reading pane is attributed to
// its action, and an unbound one is reported as unhandled. Both go through the
// real handleKey path against a stub ingest server, so this fails if the hook
// is removed, if the ladder stops resolving, or if the catalogue rejects the
// properties the hook sends.
func TestRecordKeyAttributesAndReportsUnhandled(t *testing.T) {
	in := startTelemetry(t)

	m := newTestModel()
	m.focus = focusMessages
	// `R` is react in the messages pane; ctrl+alt+q is bound to nothing.
	m.update(tea.KeyPressMsg{Code: 'R', Text: "R", Mod: tea.ModShift})
	m.update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl | tea.ModAlt})
	telemetry.Close() // flushes the snapshot and the queued events

	body := in.all()
	for _, want := range []string{
		"unhandled_key",  // the dead key was reported
		"ctrl+alt+q",     // ...naming the keystroke, which is a non-text one
		"focus:messages", // ...attributed to the pane that had focus
		"usage_snapshot", // the counted tier flushed
		"react",          // ...crediting the bound key to its action
	} {
		if !strings.Contains(body, want) {
			t.Errorf("telemetry payload missing %q\ngot: %s", want, body)
		}
	}
}

// TestRecordKeyNeverReportsTypedText is the privacy check on the hook. Typing
// into the composer must produce no keystroke report at all: not the character,
// not an "other" placeholder, nothing but the fact that time was spent there.
func TestRecordKeyNeverReportsTypedText(t *testing.T) {
	in := startTelemetry(t)

	m := newTestModel()
	m.focus = focusInput
	m.input.Focus()
	for _, r := range "secret plans" {
		m.update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	telemetry.Close()

	body := in.all()
	if strings.Contains(body, "unhandled_key") {
		t.Errorf("typing into the composer produced an unhandled_key event:\n%s", body)
	}
	for _, word := range []string{"secret", "plans"} {
		if strings.Contains(body, word) {
			t.Errorf("typed text %q reached the payload:\n%s", word, body)
		}
	}
	if !strings.Contains(body, "focus:input") {
		t.Errorf("composer time was not counted at all:\n%s", body)
	}
}

// TestRecordKeyIsNoopWhenDisabled: the default state. No client, no events, and
// the hook must not panic on a bare Model.
func TestRecordKeyIsNoopWhenDisabled(t *testing.T) {
	telemetry.Close() // ensure cold
	m := newTestModel()
	m.focus = focusMessages
	m.recordKey(tea.KeyPressMsg{Code: 'R', Text: "R", Mod: tea.ModShift})
	if telemetry.Enabled() {
		t.Fatal("telemetry reported itself enabled without consent")
	}
}

// --- helpers -------------------------------------------------------------

// startTelemetry opts an isolated config in and points the client at a stub
// ingest server, returning it so a test can assert on what was sent.
func startTelemetry(t *testing.T) *stubIngest {
	t.Helper()
	in := &stubIngest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		in.mu.Lock()
		in.bodies = append(in.bodies, string(b))
		in.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(telemetry.Close)

	t.Setenv(config.DirEnv, t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	cfg.Telemetry.Enabled = &yes
	t.Setenv(telemetry.KeyEnv, "phc_test")
	t.Setenv(telemetry.HostEnv, srv.URL)

	telemetry.Close() // drop any client a previous test left open
	telemetry.Start(cfg)
	if !telemetry.Enabled() {
		t.Fatal("telemetry did not start")
	}
	return in
}

type stubIngest struct {
	mu     sync.Mutex
	bodies []string
}

func (s *stubIngest) all() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.bodies, "\n")
}

// missing returns the elements of want that are absent from have.
func missing(want, have []string) []string {
	set := make(map[string]bool, len(have))
	for _, h := range have {
		set[h] = true
	}
	var out []string
	for _, w := range want {
		if !set[w] {
			out = append(out, w)
		}
	}
	return out
}

// TestMouseTargetsCoverHitZones: every clickable region must map to a
// catalogued target name, or its clicks would be dropped by the counter map's
// whitelist and the region would look unused.
func TestMouseTargetsCoverHitZones(t *testing.T) {
	catalogued := make(map[string]bool, len(telemetry.MouseTargets))
	for _, n := range telemetry.MouseTargets {
		catalogued[n] = true
	}
	seen := make(map[string]hitZone, len(telemetry.MouseTargets))
	// hitFeedMarkAll is the last zone in the enum; walking to it covers them all
	// and fails loudly if a new one is appended without a mapping.
	for z := hitNone; z <= hitFeedMarkAll; z++ {
		name := z.telemetryTarget()
		if z != hitNone && name == "nothing" {
			t.Errorf("hit zone %d has no telemetry target — add it to telemetryTarget", z)
			continue
		}
		if !catalogued[name] {
			t.Errorf("hit zone %d maps to %q, which is not in telemetry.MouseTargets", z, name)
		}
		if prev, dup := seen[name]; dup {
			t.Errorf("hit zones %d and %d both map to %q", prev, z, name)
		}
		seen[name] = z
	}
}

// TestPaletteIDsAreCatalogued: every ">" palette entry must carry a stable
// telemetry id from the catalogue's set, or its use would be dropped by the
// counter map's whitelist. It also guards the privacy reason the ids exist —
// three of the display names interpolate a channel name.
func TestPaletteIDsAreCatalogued(t *testing.T) {
	catalogued := make(map[string]bool, len(telemetry.PaletteIDs))
	for _, id := range telemetry.PaletteIDs {
		catalogued[id] = true
	}
	seen := map[string]string{}
	for _, c := range builtinCommands() {
		if c.tid == "" {
			t.Errorf("palette command %q has no telemetry id", c.name)
			continue
		}
		if !catalogued[c.tid] {
			t.Errorf("palette command %q uses id %q, which is not in telemetry.PaletteIDs",
				c.name, c.tid)
		}
		if prev, dup := seen[c.tid]; dup {
			t.Errorf("palette commands %q and %q share the id %q", prev, c.name, c.tid)
		}
		seen[c.tid] = c.name
	}
}

// TestSlashIDsAreCatalogued: the built-in "/" commands are counted by their own
// names, so those names have to be in the catalogue's set.
func TestSlashIDsAreCatalogued(t *testing.T) {
	catalogued := make(map[string]bool, len(telemetry.SlashIDs))
	for _, id := range telemetry.SlashIDs {
		catalogued[id] = true
	}
	for _, c := range slashRegistry() {
		if !catalogued[c.name] {
			t.Errorf("slash command %q is not in telemetry.SlashIDs", c.name)
		}
	}
}
