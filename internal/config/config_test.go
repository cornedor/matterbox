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
