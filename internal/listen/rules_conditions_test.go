package listen

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/control"
)

// attachmentPost builds the post shape an integration actually sends: an empty
// body with the content in a Slack-style attachment, as it arrives over the
// websocket (a []any of maps, not typed structs).
func attachmentPost(body string, att map[string]any) *model.Post {
	p := &model.Post{Id: "p1", ChannelId: "c1", UserId: "u-bot", Message: body}
	p.AddProp(model.PostPropsAttachments, []any{att})
	return p
}

// TestMessageMatchesAttachments is the fix for the quietest failure in the old
// matcher: a webhook posts an empty body with everything in an attachment, and
// a `message` condition never saw a word of it.
func TestMessageMatchesAttachments(t *testing.T) {
	m, err := compileMatch(MatchSpec{Message: `(?i)sev-1`})
	if err != nil {
		t.Fatalf("compileMatch: %v", err)
	}
	cases := []struct {
		name string
		post *model.Post
		want bool
	}{
		{"attachment text", attachmentPost("", map[string]any{"text": "SEV-1 database down"}), true},
		{"attachment title", attachmentPost("", map[string]any{"title": "sev-1 incident"}), true},
		{"attachment field value", attachmentPost("", map[string]any{
			"fields": []any{map[string]any{"title": "Severity", "value": "sev-1"}},
		}), true},
		{"unrelated attachment", attachmentPost("", map[string]any{"text": "all green"}), false},
		{"body still matches", &model.Post{Id: "p1", ChannelId: "c1", Message: "sev-1 in prod"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := postedEvent(t, c.post, map[string]string{"channel_type": "O", "sender_name": "@jira"})
			if got := matchPost(ev, c.post, m, "u-me", "corne", "", nil, nil, control.Status{}); got != c.want {
				t.Errorf("matchPost = %v, want %v (text %q)", got, c.want, matchText(c.post))
			}
		})
	}
}

// TestAttachmentOnlyPostIsEligible confirms an integration's empty-bodied post
// isn't dropped by the "no empty messages" guard before any rule sees it.
func TestAttachmentOnlyPostIsEligible(t *testing.T) {
	e, count := logEngine(t)
	e.rules = mustCompile(t, RuleSpec{
		Name:    "alerts",
		Match:   MatchSpec{Message: `(?i)deploy`},
		Actions: []ActionSpec{{Type: ActionLog, Text: "ALERT"}},
	})
	p := attachmentPost("", map[string]any{"text": "deploy finished"})
	e.applyRules(t.Context(), postedEvent(t, p, map[string]string{"channel_type": "O", "sender_name": "@ci"}), p)
	if count("ALERT") != 1 {
		t.Errorf("attachment-only post should reach the rules, got %d", count("ALERT"))
	}
}

// TestFromBot covers the condition that pairs with attachments: telling an
// integration's post from a person's, using only the props the post carries.
func TestFromBot(t *testing.T) {
	human := &model.Post{Id: "p1", ChannelId: "c1", UserId: "u-bob", Message: "hi"}
	bot := &model.Post{Id: "p2", ChannelId: "c1", UserId: "u-bot", Message: "hi"}
	bot.AddProp(model.PostPropsFromWebhook, "true")

	for _, c := range []struct {
		name string
		spec MatchSpec
		post *model.Post
		want bool
	}{
		{"bot required, bot post", MatchSpec{FromBot: ptrBool(true)}, bot, true},
		{"bot required, human post", MatchSpec{FromBot: ptrBool(true)}, human, false},
		{"human required, bot post", MatchSpec{FromBot: ptrBool(false)}, bot, false},
		{"human required, human post", MatchSpec{FromBot: ptrBool(false)}, human, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, err := compileMatch(c.spec)
			if err != nil {
				t.Fatalf("compileMatch: %v", err)
			}
			ev := postedEvent(t, c.post, map[string]string{"channel_type": "O"})
			if got := matchPost(ev, c.post, m, "u-me", "corne", "", nil, nil, control.Status{}); got != c.want {
				t.Errorf("matchPost = %v, want %v", got, c.want)
			}
		})
	}
}

// TestChannelType checks the conversation-kind condition, including that it
// rejects a typo at compile time rather than never matching.
func TestChannelType(t *testing.T) {
	m, err := compileMatch(MatchSpec{ChannelTypes: []string{"private", "group"}})
	if err != nil {
		t.Fatalf("compileMatch: %v", err)
	}
	p := &model.Post{Id: "p1", ChannelId: "c1", Message: "hi"}
	for letter, want := range map[string]bool{"P": true, "G": true, "O": false, "D": false} {
		ev := postedEvent(t, p, map[string]string{"channel_type": letter})
		if got := matchPost(ev, p, m, "", "", "", nil, nil, control.Status{}); got != want {
			t.Errorf("channel_type %s = %v, want %v", letter, got, want)
		}
	}
	if _, err := compileMatch(MatchSpec{ChannelTypes: []string{"priv"}}); err == nil {
		t.Error("an unknown channel type should be a compile error")
	}
}

// TestTimeWindow covers the clock condition: office hours, a window that wraps
// midnight, and a weekday restriction — each tested against the moment the
// trigger fired, not the daemon's uptime.
func TestTimeWindow(t *testing.T) {
	// 2026-08-18 is a Tuesday, 2026-08-22 a Saturday.
	at := func(day, hour, min int) time.Time {
		return time.Date(2026, 8, day, hour, min, 0, 0, time.Local)
	}
	cases := []struct {
		name string
		spec TimeSpec
		when time.Time
		want bool
	}{
		{"inside office hours", TimeSpec{After: "09:00", Before: "17:00"}, at(18, 10, 0), true},
		{"before office hours", TimeSpec{After: "09:00", Before: "17:00"}, at(18, 8, 59), false},
		{"at the exclusive end", TimeSpec{After: "09:00", Before: "17:00"}, at(18, 17, 0), false},
		{"night window wraps midnight", TimeSpec{After: "22:00", Before: "06:00"}, at(18, 2, 0), true},
		{"night window daytime", TimeSpec{After: "22:00", Before: "06:00"}, at(18, 12, 0), false},
		{"weekday only, on a Tuesday", TimeSpec{Days: []string{"mon", "tue"}}, at(18, 12, 0), true},
		{"weekday only, on a Saturday", TimeSpec{Days: []string{"mon", "tue"}}, at(22, 12, 0), false},
		{"one-sided after", TimeSpec{After: "18:00"}, at(18, 20, 0), true},
		{"one-sided after, earlier", TimeSpec{After: "18:00"}, at(18, 9, 0), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := compileMatch(MatchSpec{Time: &c.spec})
			if err != nil {
				t.Fatalf("compileMatch: %v", err)
			}
			p := &model.Post{Id: "p1", ChannelId: "c1", Message: "hi", CreateAt: c.when.UnixMilli()}
			ev := postedEvent(t, p, map[string]string{"channel_type": "O"})
			if got := matchPost(ev, p, m, "", "", "", nil, nil, control.Status{}); got != c.want {
				t.Errorf("matchPost = %v, want %v", got, c.want)
			}
		})
	}

	if _, err := compileMatch(MatchSpec{Time: &TimeSpec{}}); err == nil {
		t.Error("an empty time window should be a compile error")
	}
	if _, err := compileMatch(MatchSpec{Time: &TimeSpec{After: "9am", Before: "17:00"}}); err == nil {
		t.Error("a malformed time should be a compile error")
	}
	if _, err := compileMatch(MatchSpec{Time: &TimeSpec{Days: []string{"funday"}}}); err == nil {
		t.Error("an unknown weekday should be a compile error")
	}
}

// TestTriggerTimeBeatsPostTime checks that a reaction on an old post is tested
// against when the reaction happened, not when the post was written.
func TestTriggerTimeBeatsPostTime(t *testing.T) {
	m, err := compileMatch(MatchSpec{Time: &TimeSpec{After: "09:00", Before: "17:00"}})
	if err != nil {
		t.Fatalf("compileMatch: %v", err)
	}
	oldPost := time.Date(2026, 8, 18, 3, 0, 0, 0, time.Local)    // written at 03:00
	reactedAt := time.Date(2026, 8, 18, 11, 0, 0, 0, time.Local) // reacted to at 11:00
	p := &model.Post{Id: "p1", ChannelId: "c1", Message: "hi", CreateAt: oldPost.UnixMilli()}
	ev := postedEvent(t, p, map[string]string{
		"channel_type": "O",
		triggerAtKey:   strconv.FormatInt(reactedAt.UnixMilli(), 10),
	})
	if !matchPost(ev, p, m, "", "", "", nil, nil, control.Status{}) {
		t.Error("the window should be tested against the trigger time, not the post time")
	}
}

// TestMessageCaptures exercises the regexp submatches reaching a template: a
// numbered group through index, and a named group as a field.
func TestMessageCaptures(t *testing.T) {
	e, _ := logEngine(t)
	e.rules = mustCompile(t, RuleSpec{
		Name:  "deploys",
		Match: MatchSpec{Message: `^!deploy (?P<env>\w+) (\d+)$`},
		Actions: []ActionSpec{
			{Type: ActionStateSet, Key: `deploy:{{ .match.env }}`, Value: `{{ index .match "2" }}`},
		},
	})
	p, ev := bobEvent(t, "!deploy prod 42")
	e.applyRules(t.Context(), ev, p)

	if v, ok, _ := e.store.GetState("deploy:prod"); !ok || v != "42" {
		t.Errorf("captures should reach the template: deploy:prod = %q (present %v)", v, ok)
	}
}

// TestCapturesInExecEnv confirms the same submatches reach a script's
// environment, where most rules actually use them.
func TestCapturesInExecEnv(t *testing.T) {
	caps := captures(mustRegexp(t, `^!deploy (?P<env>\w+)$`), "!deploy prod")
	env := execEnv(envelope{Match: caps})
	var got []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "MATTERBOX_MATCH_") {
			got = append(got, kv)
		}
	}
	want := map[string]bool{"MATTERBOX_MATCH_0=!deploy prod": true, "MATTERBOX_MATCH_1=prod": true, "MATTERBOX_MATCH_ENV=prod": true}
	for _, kv := range got {
		delete(want, kv)
	}
	if len(want) > 0 {
		t.Errorf("missing capture env vars %v (got %v)", want, got)
	}
}

// TestCapturesDontLeakBetweenRules: each rule interpolates its own submatches,
// and a rule without a message condition sees an empty map rather than the
// previous rule's captures.
func TestCapturesDontLeakBetweenRules(t *testing.T) {
	e, _ := logEngine(t)
	e.rules = mustCompile(t,
		RuleSpec{
			Name:    "first",
			Match:   MatchSpec{Message: `deploy (\w+)`},
			Actions: []ActionSpec{{Type: ActionStateSet, Key: "first", Value: `{{ index .match "1" }}`}},
		},
		RuleSpec{
			Name:    "second",
			Actions: []ActionSpec{{Type: ActionStateSet, Key: "second", Value: `[{{ index .match "1" }}]`}},
		},
	)
	p, ev := bobEvent(t, "deploy prod")
	e.applyRules(t.Context(), ev, p)

	if v, _, _ := e.store.GetState("first"); v != "prod" {
		t.Errorf("first rule's capture = %q, want prod", v)
	}
	if v, _, _ := e.store.GetState("second"); v != "[]" {
		t.Errorf("second rule should see no captures, got %q", v)
	}
}

func mustRegexp(t *testing.T, expr string) *regexp.Regexp {
	t.Helper()
	re, err := compileMatch(MatchSpec{Message: expr})
	if err != nil {
		t.Fatalf("compileMatch: %v", err)
	}
	return re.messageRe
}

// TestExecEnvCarriesCreateAt keeps the post's timestamp in the exec
// environment: without it a script that needs it has to open the message cache
// and look the post up again, for a value the daemon already had.
func TestExecEnvCarriesCreateAt(t *testing.T) {
	env := execEnv(envelope{PostID: "p1", CreateAt: 1755500000000})
	if !slices.Contains(env, "MATTERBOX_CREATE_AT=1755500000000") {
		t.Errorf("MATTERBOX_CREATE_AT missing from %v", env)
	}
}
