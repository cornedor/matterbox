package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestAISearchTimeoutDefault checks that a config with no ai_search timeout
// falls back to the built-in default rather than 0 (which would mean "no
// timeout" once multiplied into a Duration).
func TestAISearchTimeoutDefault(t *testing.T) {
	c := &Config{}
	c.fillDefaults()
	if c.AISearch.TimeoutMinutes != defaultAISearchTimeoutMinutes {
		t.Errorf("default timeout = %d; want %d", c.AISearch.TimeoutMinutes, defaultAISearchTimeoutMinutes)
	}
}

// TestAISearchTimeoutParse pins the yaml key so a renamed tag can't silently
// stop honouring an on-disk timeout_minutes, and confirms an explicit value
// survives fillDefaults.
func TestAISearchTimeoutParse(t *testing.T) {
	const y = "ai_search:\n  timeout_minutes: 12\n"
	var c Config
	if err := yaml.Unmarshal([]byte(y), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.AISearch.TimeoutMinutes != 12 {
		t.Fatalf("parsed timeout = %d; want 12", c.AISearch.TimeoutMinutes)
	}
	c.fillDefaults()
	if c.AISearch.TimeoutMinutes != 12 {
		t.Errorf("fillDefaults clobbered explicit timeout: got %d", c.AISearch.TimeoutMinutes)
	}
}

// TestNavModifierDefault: an absent keybindings section defaults the arrow-nav
// modifier to ctrl, so existing users keep the ctrl+arrow aliases.
func TestNavModifierDefault(t *testing.T) {
	c := &Config{}
	c.fillDefaults()
	if c.Keybindings.NavModifier != "ctrl" {
		t.Errorf("default nav_modifier = %q; want \"ctrl\"", c.Keybindings.NavModifier)
	}
}

// TestNavModifierParse pins the yaml key and confirms an explicit value
// survives fillDefaults (e.g. super for the macOS ⌘ key).
func TestNavModifierParse(t *testing.T) {
	const y = "keybindings:\n  nav_modifier: super\n"
	var c Config
	if err := yaml.Unmarshal([]byte(y), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	c.fillDefaults()
	if c.Keybindings.NavModifier != "super" {
		t.Errorf("fillDefaults clobbered explicit nav_modifier: got %q", c.Keybindings.NavModifier)
	}
}

// TestVimNavDefault: an absent vim_nav defaults to "global", preserving the
// ctrl+h/j/k/l-navigate-anywhere behaviour.
func TestVimNavDefault(t *testing.T) {
	c := &Config{}
	c.fillDefaults()
	if c.Keybindings.VimNav != "global" {
		t.Errorf("default vim_nav = %q; want \"global\"", c.Keybindings.VimNav)
	}
}

// TestVimNavParse pins the yaml key and confirms an explicit value survives
// fillDefaults.
func TestVimNavParse(t *testing.T) {
	const y = "keybindings:\n  vim_nav: reading\n"
	var c Config
	if err := yaml.Unmarshal([]byte(y), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	c.fillDefaults()
	if c.Keybindings.VimNav != "reading" {
		t.Errorf("fillDefaults clobbered explicit vim_nav: got %q", c.Keybindings.VimNav)
	}
}

// TestBindingsStringOrList: a binding value accepts a scalar, a list, or an
// empty value (unbind), so all three on-disk forms round-trip.
func TestBindingsStringOrList(t *testing.T) {
	const y = "keybindings:\n" +
		"  bindings:\n" +
		"    compose: a\n" +
		"    channel_next: [ctrl+j, alt+j]\n" +
		"    quit: []\n"
	var c Config
	if err := yaml.Unmarshal([]byte(y), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := c.Keybindings.Bindings["compose"]; len(got) != 1 || got[0] != "a" {
		t.Errorf("compose = %v, want [a] (scalar → one element)", got)
	}
	if got := c.Keybindings.Bindings["channel_next"]; len(got) != 2 || got[0] != "ctrl+j" || got[1] != "alt+j" {
		t.Errorf("channel_next = %v, want [ctrl+j alt+j]", got)
	}
	if got, ok := c.Keybindings.Bindings["quit"]; !ok || len(got) != 0 {
		t.Errorf("quit = %v (ok=%v), want an empty slice (unbind)", got, ok)
	}
}

// TestNavModifierLegacyMigration: a pre-NavModifier config that set
// ctrl_arrow_nav: false migrates to nav_modifier: none, and the superseded
// toggle is dropped so a rewrite carries only the new key.
func TestNavModifierLegacyMigration(t *testing.T) {
	const y = "keybindings:\n  ctrl_arrow_nav: false\n"
	var c Config
	if err := yaml.Unmarshal([]byte(y), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	c.fillDefaults()
	if c.Keybindings.NavModifier != "none" {
		t.Errorf("legacy ctrl_arrow_nav: false migrated to %q; want \"none\"", c.Keybindings.NavModifier)
	}
	if c.Keybindings.CtrlArrowNav != nil {
		t.Errorf("legacy toggle not cleared after migration: got %v", c.Keybindings.CtrlArrowNav)
	}
}

// TestRulesScalarOrList confirms the rules schema accepts channel/author as
// either a single scalar or a list, decodes the new action fields, and recurses
// into a not: block — the parts a future change must not break.
func TestRulesScalarOrList(t *testing.T) {
	const y = `
rules:
  - name: scalar form
    match:
      channel: ops
      author: alice
    actions:
      - type: notify
        urgent: true
        chat_id: "-100"
  - name: list form
    match:
      channel: [ops, "eng-*"]
      author: [alice, bob]
      not:
        author: bot
    actions:
      - type: webhook
        url: https://example.com/hook
        headers:
          Authorization: "Bearer ${TOKEN}"
`
	var c Config
	if err := yaml.Unmarshal([]byte(y), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(c.Rules) != 2 {
		t.Fatalf("want 2 rules, got %d", len(c.Rules))
	}

	scalar := c.Rules[0]
	if len(scalar.Match.Channel) != 1 || scalar.Match.Channel[0] != "ops" {
		t.Errorf("scalar channel = %v, want [ops]", scalar.Match.Channel)
	}
	if len(scalar.Match.Author) != 1 || scalar.Match.Author[0] != "alice" {
		t.Errorf("scalar author = %v, want [alice]", scalar.Match.Author)
	}
	if a := scalar.Actions[0]; !a.Urgent || a.ChatID != "-100" {
		t.Errorf("notify action overrides not parsed: %+v", a)
	}

	list := c.Rules[1]
	if len(list.Match.Channel) != 2 || list.Match.Channel[1] != "eng-*" {
		t.Errorf("list channel = %v, want [ops eng-*]", list.Match.Channel)
	}
	if len(list.Match.Author) != 2 {
		t.Errorf("list author = %v, want two entries", list.Match.Author)
	}
	if list.Match.Not == nil || len(list.Match.Not.Author) != 1 || list.Match.Not.Author[0] != "bot" {
		t.Errorf("not: block not parsed: %+v", list.Match.Not)
	}
	if h := list.Actions[0].Headers["Authorization"]; h != "Bearer ${TOKEN}" {
		t.Errorf("webhook header not parsed: %q", h)
	}
}

// TestRulesStateAndFrequency confirms the frequency block, the state match
// (single mapping and list forms), and the state_* action fields all decode.
func TestRulesStateAndFrequency(t *testing.T) {
	const y = `
rules:
  - name: single state condition
    match:
      state:
        key: failures
        gte: 3
      frequency:
        count: 3
        within: 10m
        by: author
    actions:
      - type: state_incr
        key: "failures:{{ .author }}"
        by: 2
  - name: list of state conditions
    match:
      state:
        - { key: failures, gte: 1 }
        - { key: muted, ne: "true" }
    actions:
      - type: state_set
        key: last
        value: "{{ .create_at }}"
`
	var c Config
	if err := yaml.Unmarshal([]byte(y), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(c.Rules) != 2 {
		t.Fatalf("want 2 rules, got %d", len(c.Rules))
	}

	one := c.Rules[0]
	if len(one.Match.State) != 1 || one.Match.State[0].Key != "failures" || one.Match.State[0].Gte == nil || *one.Match.State[0].Gte != 3 {
		t.Errorf("single state condition not parsed: %+v", one.Match.State)
	}
	if one.Match.Frequency == nil || one.Match.Frequency.Count != 3 || one.Match.Frequency.Within != "10m" || one.Match.Frequency.By != "author" {
		t.Errorf("frequency not parsed: %+v", one.Match.Frequency)
	}
	if a := one.Actions[0]; a.Key != "failures:{{ .author }}" || a.By == nil || *a.By != 2 {
		t.Errorf("state_incr fields not parsed: %+v", a)
	}

	two := c.Rules[1]
	if len(two.Match.State) != 2 || two.Match.State[1].Ne == nil || *two.Match.State[1].Ne != "true" {
		t.Errorf("state condition list not parsed: %+v", two.Match.State)
	}
	if a := two.Actions[0]; a.Key != "last" || a.Value != "{{ .create_at }}" {
		t.Errorf("state_set fields not parsed: %+v", a)
	}
}

// TestRulesYAMLAnchor confirms a YAML anchor/alias works for sharing a regexp
// between the arm and tick rules (the hot-mention pattern in docs/rules.md).
func TestRulesYAMLAnchor(t *testing.T) {
	const y = `
rules:
  - name: arm
    match:
      message: &terms "(?i)urgent|sev-1"
    actions: [ { type: state_set, key: "hot:{{ .channel_id }}", value: "5" } ]
  - name: tick
    match:
      state: { key: "hot:{{ .channel_id }}", gte: 1 }
      not: { message: *terms }
    actions: [ { type: state_incr, key: "hot:{{ .channel_id }}", by: -1 } ]
`
	var c Config
	if err := yaml.Unmarshal([]byte(y), &c); err != nil {
		t.Fatalf("anchor parse: %v", err)
	}
	if c.Rules[0].Match.Message != "(?i)urgent|sev-1" {
		t.Fatalf("arm message = %q", c.Rules[0].Match.Message)
	}
	if c.Rules[1].Match.Not == nil || c.Rules[1].Match.Not.Message != "(?i)urgent|sev-1" {
		t.Fatalf("alias not resolved: %+v", c.Rules[1].Match.Not)
	}
}
