package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/textinput"
	"github.com/mattermost/mattermost/server/public/model"
)

// userArgModel returns a Model sitting in a username arg prompt with value v.
func userArgModel(v string) Model {
	ti := textinput.New()
	ti.SetValue(v)
	return Model{
		switcher:           &ti,
		width:              80,
		switcherCmdPending: &switcherCommand{name: "Start group DM", userArgs: true},
		me:                 &model.User{Id: "me", Username: "corne"},
		userNames:          map[string]string{"me": "corne", "a": "alice", "b": "bob", "c": "alfred"},
	}
}

func TestUserArgToken(t *testing.T) {
	for v, want := range map[string][2]string{
		"":              {"", ""},
		"al":            {"", "al"},
		"@al":           {"", "al"},
		"@alice, @b":    {"@alice,", "b"},
		"@alice, @bob,": {"@alice, @bob,", ""},
	} {
		head, token := userArgToken(v)
		if head != want[0] || token != want[1] {
			t.Errorf("userArgToken(%q) = %q, %q; want %q, %q", v, head, token, want[0], want[1])
		}
	}
}

// TestUserArgAccept checks completion replaces the partial token, skips names
// already listed and yourself, and leaves a separator for the next name.
func TestUserArgAccept(t *testing.T) {
	m := userArgModel("@alice, al")
	for _, u := range m.userArgMatches() {
		if u.Username == "alice" || u.Username == "corne" {
			t.Fatalf("match list offers %q, already listed or self", u.Username)
		}
	}
	if !m.acceptUserArg() {
		t.Fatal("acceptUserArg found nothing for \"al\"")
	}
	if got := m.switcher.Value(); got != "@alice, @alfred, " {
		t.Errorf("value = %q, want %q", got, "@alice, @alfred, ")
	}
	if m.userArgMatches() != nil {
		t.Error("empty token should offer no matches")
	}
}

func TestUserArgRendersMatches(t *testing.T) {
	m := userArgModel("bo")
	out := m.renderSwitcherArgPrompt(60, 56)
	if !strings.Contains(out, "@bob") {
		t.Errorf("match missing from prompt:\n%s", out)
	}
}
