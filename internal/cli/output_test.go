package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/spf13/cobra"
)

// decodeLines parses NDJSON output into jsonPost records, failing on any
// malformed line so a stray non-JSON write (a header leaking onto stdout) is
// caught.
func decodeLines(t *testing.T, s string) []jsonPost {
	t.Helper()
	var out []jsonPost
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if line == "" {
			continue
		}
		var jp jsonPost
		if err := json.Unmarshal([]byte(line), &jp); err != nil {
			t.Fatalf("line %q is not valid JSON: %v", line, err)
		}
		out = append(out, jp)
	}
	return out
}

func TestWriteJSONPosts(t *testing.T) {
	at := ms(t, "09:05")
	posts := []*model.Post{
		{Id: "p1", ChannelId: "c1", UserId: "u1", Message: "hello", CreateAt: at},
		{Id: "p2", ChannelId: "c1", UserId: "u2", Message: "reply", CreateAt: at + 1000, RootId: "p1"},
		{Id: "p3", ChannelId: "c1", UserId: "u3", Message: "who", CreateAt: at + 2000}, // unknown author
	}
	names := map[string]string{"u1": "alice", "u2": "bob"}
	lbl := channelLabeler(func(string) string { return "eng/general" })

	var buf bytes.Buffer
	if err := writeJSONPosts(&buf, lbl, names, posts); err != nil {
		t.Fatalf("writeJSONPosts: %v", err)
	}
	got := decodeLines(t, buf.String())
	if len(got) != 3 {
		t.Fatalf("got %d lines, want 3", len(got))
	}

	if got[0].Username != "alice" || got[0].Channel != "eng/general" || got[0].Message != "hello" {
		t.Errorf("line 0 = %+v", got[0])
	}
	if got[0].CreateAt != at {
		t.Errorf("create_at = %d, want %d", got[0].CreateAt, at)
	}
	// Time must denote the same instant as create_at (tz-independent assertion).
	parsed, err := time.Parse(time.RFC3339, got[0].Time)
	if err != nil {
		t.Fatalf("time %q not RFC3339: %v", got[0].Time, err)
	}
	if parsed.Unix() != at/1000 {
		t.Errorf("time %q = unix %d, want %d", got[0].Time, parsed.Unix(), at/1000)
	}
	// root_id is present only on the reply.
	if got[0].RootID != "" {
		t.Errorf("line 0 root_id = %q, want empty", got[0].RootID)
	}
	if got[1].RootID != "p1" {
		t.Errorf("line 1 root_id = %q, want p1", got[1].RootID)
	}
	// Unknown author resolves to an empty username (consumer decides), not the
	// text path's "unknown" sentinel.
	if got[2].Username != "" {
		t.Errorf("unknown author username = %q, want empty", got[2].Username)
	}
}

// TestWriteJSONPostsRootIDOmitted asserts the omitempty actually drops root_id
// from the serialized line (not just decodes to "").
func TestWriteJSONPostsRootIDOmitted(t *testing.T) {
	var buf bytes.Buffer
	err := writeJSONPosts(&buf, func(string) string { return "x" }, nil,
		[]*model.Post{{Id: "p1", CreateAt: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "root_id") {
		t.Errorf("root_id should be omitted for a top-level post: %s", buf.String())
	}
}

// TestToJSONPostOverrideName confirms a webhook/bot override_username wins over
// the id→name map, matching the text formatter.
func TestToJSONPostOverrideName(t *testing.T) {
	p := &model.Post{Id: "p", UserId: "u1", Message: "beep", CreateAt: 1}
	p.AddProp("override_username", "ci-bot")
	jp := toJSONPost(p, func(string) string { return "c" }, map[string]string{"u1": "alice"})
	if jp.Username != "ci-bot" {
		t.Errorf("username = %q, want ci-bot (override wins)", jp.Username)
	}
}

// TestWriteJSONPostsNoHTMLEscape ensures message text round-trips verbatim
// rather than coming back as < etc.
func TestWriteJSONPostsNoHTMLEscape(t *testing.T) {
	var buf bytes.Buffer
	err := writeJSONPosts(&buf, func(string) string { return "c" }, nil,
		[]*model.Post{{Id: "p", Message: "a < b && c > d", CreateAt: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "a < b && c > d") {
		t.Errorf("message was HTML-escaped: %s", buf.String())
	}
}

func TestAddOutputFlags(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    bool
		wantErr bool
	}{
		{"default text", nil, false, false},
		{"--json", []string{"--json"}, true, false},
		{"-o json", []string{"-o", "json"}, true, false},
		{"--output JSON case-insensitive", []string{"--output", "JSON"}, true, false},
		{"-o text", []string{"-o", "text"}, false, false},
		{"-o yaml errors", []string{"-o", "yaml"}, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := &cobra.Command{Use: "x"}
			resolve := addOutputFlags(cmd)
			if err := cmd.ParseFlags(c.args); err != nil {
				t.Fatalf("ParseFlags: %v", err)
			}
			got, err := resolve()
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if err == nil && got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}
