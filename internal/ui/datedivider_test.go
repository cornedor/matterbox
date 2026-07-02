package ui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

// dayMillis is the create-time of noon on the given local calendar day, so the
// day a post falls on is unambiguous regardless of the machine's timezone.
func dayMillis(y int, mo time.Month, d int) int64 {
	return time.Date(y, mo, d, 12, 0, 0, 0, time.Local).UnixMilli()
}

// dateRuleRows returns the indices of the rendered rows carrying a date
// separator. A date rule is the only ─ rule whose label holds a comma (the
// weekday/date labels do; the "unread messages" rule and header times don't).
func dateRuleRows(content string) []int {
	var rows []int
	for i, l := range strings.Split(content, "\n") {
		if strings.Contains(l, "─") && strings.Contains(l, ",") {
			rows = append(rows, i)
		}
	}
	return rows
}

// TestCrossesLocalDay covers the adjacency test that places a date rule: the
// first post starts a day, same-day posts don't, and any calendar rollover does.
func TestCrossesLocalDay(t *testing.T) {
	morning := pPost("a", dayMillis(2020, time.March, 2), "u")
	evening := pPost("b", dayMillis(2020, time.March, 2)+8*3600*1000, "u")
	nextDay := pPost("c", dayMillis(2020, time.March, 3), "u")
	nextYear := pPost("d", dayMillis(2021, time.January, 1), "u")

	if !crossesLocalDay(nil, morning) {
		t.Error("first loaded post should open a day")
	}
	if crossesLocalDay(morning, evening) {
		t.Error("same local day should not cross")
	}
	if !crossesLocalDay(evening, nextDay) {
		t.Error("next calendar day should cross")
	}
	if !crossesLocalDay(nextDay, nextYear) {
		t.Error("year rollover should cross")
	}
	if crossesLocalDay(morning, nil) {
		t.Error("nil cur should never cross")
	}
}

// TestFormatDividerDate checks the label branches: relative "Today"/"Yesterday"
// for the two most recent days, a full date with year for other years, and no
// year for another day in the current year.
func TestFormatDividerDate(t *testing.T) {
	now := time.Now()
	if got := formatDividerDate(now.UnixMilli()); got != "Today" {
		t.Errorf("today: got %q, want Today", got)
	}
	if got := formatDividerDate(now.AddDate(0, 0, -1).UnixMilli()); got != "Yesterday" {
		t.Errorf("yesterday: got %q, want Yesterday", got)
	}
	// A date in a clearly different year always spells the year out. March 2,
	// 2020 was a Monday.
	if got := formatDividerDate(dayMillis(2020, time.March, 2)); got != "Monday, March 2, 2020" {
		t.Errorf("cross-year: got %q, want %q", got, "Monday, March 2, 2020")
	}
	// A day well inside the current year (and not the last two days) omits the
	// year. Skip in the rare window where "10 days ago" fell into last year.
	if d := now.AddDate(0, 0, -10); d.Year() == now.Year() {
		got := formatDividerDate(d.UnixMilli())
		if strings.Contains(got, strconv.Itoa(d.Year())) {
			t.Errorf("same-year label should omit the year: %q", got)
		}
		if !strings.Contains(got, d.Format("Monday")) {
			t.Errorf("same-year label %q missing weekday %q", got, d.Format("Monday"))
		}
	}
}

// TestDateDividerBetweenDays: with posts spanning two local days and separators
// enabled, renderMessages draws a rule above the opening post and another above
// the first post of the second day, and the row geometry stays consistent.
func TestDateDividerBetweenDays(t *testing.T) {
	posts := []*model.Post{
		pPost("a", dayMillis(2020, time.March, 2), "u"),
		pPost("b", dayMillis(2020, time.March, 2)+3600*1000, "u"),
		pPost("c", dayMillis(2020, time.March, 3), "u"),
	}
	m := pagingModel(posts, len(posts)-1)
	m.showDateSeparators = true
	m.renderMessages()

	content := m.msgsView.GetContent()
	rules := dateRuleRows(content)
	if len(rules) != 2 {
		t.Fatalf("want 2 date rules (top + day boundary), got %d:\n%s", len(rules), content)
	}
	lines := strings.Split(content, "\n")
	// The second rule sits in the gap directly above post c (index 2);
	// msgRowStarts[2] still points at c's real first line, one row below it.
	boundary := rules[1]
	if got := m.msgRowStarts[2]; got != boundary+1 {
		t.Errorf("day rule not directly above first post of day 2: rule row %d, post c starts at %d", boundary, got)
	}
	if want := formatDividerDate(posts[2].CreateAt); !strings.Contains(lines[boundary], want) {
		t.Errorf("boundary rule %q missing label %q", lines[boundary], want)
	}
	// Row accounting stays consistent: the final rowStart equals the line count.
	if total := m.msgRowStarts[len(m.msgRowStarts)-1]; total != len(lines) {
		t.Errorf("row geometry off: total rows %d, content lines %d", total, len(lines))
	}
}

// TestDateDividerDisabled: with the toggle off, no date rule is drawn.
func TestDateDividerDisabled(t *testing.T) {
	posts := []*model.Post{
		pPost("a", dayMillis(2020, time.March, 2), "u"),
		pPost("c", dayMillis(2020, time.March, 3), "u"),
	}
	m := pagingModel(posts, len(posts)-1)
	m.showDateSeparators = false
	m.renderMessages()
	if rules := dateRuleRows(m.msgsView.GetContent()); len(rules) != 0 {
		t.Fatalf("date rules drawn while disabled: rows %v", rules)
	}
}

// TestDateDividerBreaksGrouping: two posts by the same author within the group
// window but straddling local midnight must not collapse into one run — the
// second day's first post keeps its header beneath the date rule.
func TestDateDividerBreaksGrouping(t *testing.T) {
	d1 := time.Date(2020, time.March, 2, 23, 59, 30, 0, time.Local)
	d2 := d1.Add(60 * time.Second) // 2020-03-03 00:00:30 — next local day, 60s later
	posts := []*model.Post{
		pPost("a", d1.UnixMilli(), "bob"),
		pPost("b", d2.UnixMilli(), "bob"),
	}
	m := pagingModel(posts, 1)
	m.userNames = map[string]string{"bob": "bob"}
	m.groupWindow = 120 * time.Second
	m.showDateSeparators = true
	m.renderMessages()

	lines := strings.Split(m.msgsView.GetContent(), "\n")
	start := m.msgRowStarts[1] // post b's first rendered row
	if !strings.Contains(lines[start], "bob") {
		t.Errorf("post b should keep its header under the date rule, got %q", lines[start])
	}
}
