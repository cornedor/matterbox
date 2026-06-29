package cli

import (
	"bufio"
	"strings"
	"testing"
)

// Token extraction now lives in internal/mmauth (TestExtractToken there).

func TestPromptLine(t *testing.T) {
	// A normal newline-terminated line is read and trimmed; the prompt is written.
	in := bufio.NewReader(strings.NewReader("  alice  \n"))
	var out strings.Builder
	got, err := promptLine(in, &out, "user: ")
	if err != nil {
		t.Fatalf("promptLine: %v", err)
	}
	if got != "alice" {
		t.Errorf("line = %q, want alice", got)
	}
	if !strings.Contains(out.String(), "user: ") {
		t.Errorf("prompt not written, out = %q", out.String())
	}

	// A final line ending at EOF (no trailing newline) still counts.
	in = bufio.NewReader(strings.NewReader("123456"))
	got, err = promptLine(in, &out, "code: ")
	if err != nil {
		t.Fatalf("promptLine (EOF): %v", err)
	}
	if got != "123456" {
		t.Errorf("EOF line = %q, want 123456", got)
	}
}

func TestFingerprint(t *testing.T) {
	if got := fingerprint("abcdef1234567890wxyz"); got != "abcdef…wxyz" {
		t.Errorf("fingerprint long = %q", got)
	}
	// Short tokens are fully masked rather than revealing head/tail.
	if got := fingerprint("short"); got != "•••••" {
		t.Errorf("fingerprint short = %q", got)
	}
}
