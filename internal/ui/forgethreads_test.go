package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"matterbox/internal/forge"
)

var threadWhen = time.Date(2026, 9, 18, 14, 3, 0, 0, time.UTC)

// panelThreads is a merge request with the three shapes of conversation: an
// open inline one, a resolved one on a line that no longer exists, and one on
// the merge request itself.
func panelThreads() []forge.Thread {
	return []forge.Thread{
		{ID: "a", Path: "internal/ui/diffview.go", NewLine: 412, Notes: []forge.Note{
			{Author: "Grace Hopper", Body: "why the parens?", Created: threadWhen},
			{Author: "Ada Lovelace", Body: "gofmt wants them", Created: threadWhen.Add(time.Hour)},
		}},
		{ID: "b", Path: "internal/forge/diff.go", OldLine: 88, Resolved: true, Notes: []forge.Note{
			{Author: "Bram", Body: "this line is gone now", Created: threadWhen},
		}},
		{ID: "c", Notes: []forge.Note{
			{Author: "Bram", Body: "Looks good overall, two nits above.", Created: threadWhen},
		}},
	}
}

// openPanelWithThreads opens the reference panel on a merge request and lands a
// fetch carrying its conversation, as the real Cmd would.
func openPanelWithThreads(t *testing.T, threads []forge.Thread) Model {
	t.Helper()
	m := configuredForgeModel(t)
	ch := sampleMR()
	m = openLoadedChange(t, m, forgeGitLab, mrLink, ch)
	updated, _ := m.handleForgeLoaded(forgeLoadedMsg{
		gen: m.refGen, provider: forgeGitLab, repo: ch.Repo, number: ch.Number,
		change: ch, threads: threads,
	})
	return updated.(Model)
}

func TestRefPanelListsEveryThread(t *testing.T) {
	m := openPanelWithThreads(t, panelThreads())
	out := ansi.Strip(m.renderForgeChange(m.forgeAt(forgeGitLab), m.refChange, 80))

	for _, want := range []string{
		"Discussion (3)",
		"2 inline",
		"internal/ui/diffview.go:412", // where the inline note hangs
		"internal/forge/diff.go:88",   // the one on a removed line
		"(removed)",                   // …said out loud, since the line is gone
		"on the change request",       // the unanchored one
		"Grace Hopper",
		"why the parens?",
		"gofmt wants them",
		"Looks good overall",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("panel is missing %q:\n%s", want, out)
		}
	}
	// A resolved thread is marked.
	if !strings.Contains(out, "✓") {
		t.Error("no resolved marker")
	}
	if !strings.Contains(out, "2 unresolved") {
		t.Errorf("no unresolved count:\n%s", out)
	}
	// The timestamp rides with the author, like the Jira panel's comments.
	if !strings.Contains(out, "2026-09-18 14:03") {
		t.Error("no timestamp on a note")
	}
	// Each note starts its own line: markdown leaves no trailing newline, so a
	// reply used to run onto the end of the note above it.
	if strings.Contains(out, "?Ada Lovelace") {
		t.Errorf("a reply ran into the note above it:\n%s", out)
	}
}

func TestRefPanelNoThreadsNoSection(t *testing.T) {
	m := openPanelWithThreads(t, nil)
	out := ansi.Strip(m.renderForgeChange(m.forgeAt(forgeGitLab), m.refChange, 80))
	if strings.Contains(out, "Discussion") {
		t.Errorf("empty discussion section drawn:\n%s", out)
	}
}

// A long conversation is capped in both directions, and says so.
func TestRefPanelCapsTheConversation(t *testing.T) {
	var threads []forge.Thread
	for i := 0; i < forgeThreadsMax+3; i++ {
		threads = append(threads, forge.Thread{ID: "t", Notes: []forge.Note{{Author: "Bram", Body: "x"}}})
	}
	threads[0].Notes = nil
	for i := 0; i < forgeThreadNotesMax+2; i++ {
		threads[0].Notes = append(threads[0].Notes, forge.Note{Author: "Ada", Body: "y"})
	}
	m := openPanelWithThreads(t, threads)
	out := ansi.Strip(m.renderForgeChange(m.forgeAt(forgeGitLab), m.refChange, 80))
	if !strings.Contains(out, "…and 2 more replies") {
		t.Errorf("replies not capped:\n%s", out)
	}
	if !strings.Contains(out, "…and 3 more") {
		t.Errorf("threads not capped:\n%s", out)
	}
}

// The panel's own fetch asks a forge that can serve conversations for them, and
// leaves the badge path (Provider.Get) alone.
func TestRefPanelFetchesThreads(t *testing.T) {
	stub := &stubReviewer{threads: panelThreads()}
	m := configuredForgeModel(t)
	m = openDiffStub(t, m, stub)
	msg := m.fetchForgeChange(1, forgeGitLab, "g/p", 5, "")().(forgeLoadedMsg)
	if msg.err != nil {
		t.Fatalf("fetch: %v", msg.err)
	}
	if len(msg.threads) != 3 {
		t.Errorf("threads = %d, want 3", len(msg.threads))
	}
	if stub.threadCall != 1 || stub.getCalls != 1 {
		t.Errorf("calls: Get %d, Threads %d, want one each", stub.getCalls, stub.threadCall)
	}
}

// An issue has no diff and no inline notes; it must not cost a discussions
// request either.
func TestRefPanelSkipsThreadsForAnIssue(t *testing.T) {
	stub := &stubReviewer{threads: panelThreads(), change: sampleIssue()}
	m := configuredForgeModel(t)
	m = openDiffStub(t, m, stub)
	msg := m.fetchForgeChange(1, forgeGitLab, "g/p", 5, "")().(forgeLoadedMsg)
	if len(msg.threads) != 0 || stub.threadCall != 0 {
		t.Errorf("an issue fetched %d threads (%d calls)", len(msg.threads), stub.threadCall)
	}
}
