package ui

import "testing"

// BenchmarkComposerKeystroke measures the per-keystroke render cost of typing in
// the composer of a busy channel. bubbletea rebuilds View() after every key and
// update() clears the frame memo each time, so this re-renders viewContent() with
// a slightly different (same-length, so the input height and message viewport
// stay put) draft each iteration — the steady-state typing path that the
// scrollback cache is meant to accelerate. navModel() lands on a team tab so the
// messages pane (not the feed) is rendered.
func BenchmarkComposerKeystroke(b *testing.B) {
	m := navModel()
	m.vcache = &viewCache{}
	m.width, m.height = 120, 40
	posts, names := benchPosts(400)
	m.posts = posts
	m.userNames = names
	m.postIdx = len(posts) - 1
	m.focus = focusInput
	m.resizeMessagesViewport() // lays out panes + fills the viewport from posts

	const base = "the quick brown fox jumps over the lazy dog "
	vals := []string{base + "x", base + "y"} // equal length: no input reflow

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.input.SetValue(vals[i&1])
		m.syncInputHeight()
		m.vcache.viewValid = false // update() invalidates the memo every keystroke
		_ = m.viewContent()
	}
}
