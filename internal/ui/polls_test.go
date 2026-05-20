package ui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// rawActivePoll mirrors the JSON shape persisted for an in-flight
// matterpoll poll (the one from the HR - Partysquad channel).
const rawActivePoll = `{
  "id":"7m4dpkd4jirxxnjemouu83apih",
  "create_at":1779116596627,
  "type":"custom_matterpoll",
  "message":"",
  "props":{
    "attachments":[{
      "actions":[
        {"id":"vote0","name":"Levend stratego","type":"button"},
        {"id":"vote1","name":"Prison Island","type":"button"},
        {"id":"vote2","name":"VR bowlen","type":"button"},
        {"id":"addOption","name":"Add Option","type":"button"},
        {"id":"deletePoll","name":"Delete Poll","type":"custom_matterpoll_admin_button"},
        {"id":"endPoll","name":"End Poll","type":"custom_matterpoll_admin_button"}
      ],
      "author_name":"corne",
      "text":"---\n**Total votes**: 3",
      "title":"Wat gaan we doen?"
    }],
    "poll_id":"o4sn9srnqjnoz85uafk6dfp4so"
  }
}`

const rawEndedPoll = `{
  "id":"ipon43tgp7r13kzw8jqf3e1uir",
  "type":"custom_matterpoll",
  "props":{
    "attachments":[{
      "fields":[
        {"short":true,"title":"broertje (1 vote)","value":"@cdorrestijn"},
        {"short":true,"title":"zusje (4 votes)","value":"@a, @b, @c and @d"}
      ],
      "text":"This poll has ended. The results are:",
      "title":"Rens krijgt een..."
    }]
  }
}`

func unmarshalPost(t *testing.T, raw string) *model.Post {
	t.Helper()
	var p model.Post
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &p
}

func TestIsPollDetectsActiveAndEnded(t *testing.T) {
	if !isPoll(unmarshalPost(t, rawActivePoll)) {
		t.Fatal("active poll not detected")
	}
	if !isPoll(unmarshalPost(t, rawEndedPoll)) {
		t.Fatal("ended poll not detected")
	}
	plain := &model.Post{Message: "hello"}
	if isPoll(plain) {
		t.Fatal("plain post falsely detected as poll")
	}
}

func TestExtractPollAssignsAccelerators(t *testing.T) {
	pd := extractPoll(unmarshalPost(t, rawActivePoll))
	if pd == nil {
		t.Fatal("extractPoll returned nil")
	}
	if pd.title != "Wat gaan we doen?" {
		t.Errorf("title = %q", pd.title)
	}
	if pd.authorName != "corne" {
		t.Errorf("author = %q", pd.authorName)
	}
	// Expect vote actions to get '1', '2', '3' and the admin buttons
	// to keep their letter accelerators.
	want := map[string]rune{
		"vote0":      '1',
		"vote1":      '2',
		"vote2":      '3',
		"addOption":  'a',
		"endPoll":    'E',
		"deletePoll": 'X',
	}
	for _, a := range pd.actions {
		got, ok := want[a.id]
		if !ok {
			t.Errorf("unexpected action id %q", a.id)
			continue
		}
		if a.accel != got {
			t.Errorf("action %s accel = %q, want %q", a.id, a.accel, got)
		}
	}
	if pd.ended {
		t.Error("active poll flagged as ended")
	}
}

func TestExtractPollEndedHasFields(t *testing.T) {
	pd := extractPoll(unmarshalPost(t, rawEndedPoll))
	if pd == nil {
		t.Fatal("extractPoll returned nil")
	}
	if !pd.ended {
		t.Fatal("ended poll not flagged as ended")
	}
	if len(pd.fields) != 2 {
		t.Fatalf("len(fields) = %d, want 2", len(pd.fields))
	}
	if !strings.Contains(pd.fields[0].Title, "broertje") {
		t.Errorf("field[0].Title = %q", pd.fields[0].Title)
	}
}

func TestPollActionByKeyResolvesDigitsAndLetters(t *testing.T) {
	p := unmarshalPost(t, rawActivePoll)
	cases := []struct {
		key  rune
		want string
	}{
		{'1', "vote0"},
		{'2', "vote1"},
		{'3', "vote2"},
		{'a', "addOption"},
		{'E', "endPoll"},
		{'X', "deletePoll"},
	}
	for _, tc := range cases {
		a, ok := pollActionByKey(p, tc.key)
		if !ok {
			t.Errorf("key %q not handled", tc.key)
			continue
		}
		if a.id != tc.want {
			t.Errorf("key %q → %q, want %q", tc.key, a.id, tc.want)
		}
	}
	if _, ok := pollActionByKey(p, 'z'); ok {
		t.Error("unhandled key z resolved")
	}
}

func TestRenderPollIncludesOptions(t *testing.T) {
	m := Model{}
	out := m.renderPoll(unmarshalPost(t, rawActivePoll), 80, true)
	joined := strings.Join(out, "\n")
	for _, want := range []string{
		"Wat gaan we doen?",
		"Levend stratego",
		"Prison Island",
		"VR bowlen",
		"Add Option",
		"End Poll",
		"Delete Poll",
		"Total votes",
		"vote",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("renderPoll output missing %q\nfull:\n%s", want, joined)
		}
	}
}

func TestRenderPollEndedShowsResults(t *testing.T) {
	m := Model{}
	out := m.renderPoll(unmarshalPost(t, rawEndedPoll), 80, false)
	joined := strings.Join(out, "\n")
	for _, want := range []string{
		"Rens krijgt een...",
		"broertje (1 vote)",
		"zusje (4 votes)",
		"@cdorrestijn",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("renderPoll (ended) missing %q\nfull:\n%s", want, joined)
		}
	}
}
