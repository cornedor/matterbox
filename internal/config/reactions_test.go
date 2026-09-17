package config

import (
	"testing"

	"matterbox/internal/emoji"
)

// TestDefaultReactionsResolve holds defaultReactions to what its comment
// claims: every name is one Mattermost knows, so the picker never offers a
// reaction the server would refuse.
func TestDefaultReactionsResolve(t *testing.T) {
	for _, n := range defaultReactions {
		if emoji.Glyph(n) == "" {
			t.Errorf("default reaction %q is not a Mattermost shortcode", n)
		}
	}
}
