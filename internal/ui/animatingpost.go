package ui

// animatingPost reports whether postID is the target of a running
// animation: the typing reveal (typing.go) or a live game world post
// (gorillas.go, kurve.go). Every one of those drives its animation by
// editing a single post once per frame, so each owner contributes its
// own clause here.
//
// Frame edits on these posts are intentionally not persisted: the
// per-frame churn would otherwise flood the local edit-history
// (post_revisions, captured by a posts UPDATE trigger) and pollute the
// message cache with throwaway frames.
func (m *Model) animatingPost(postID string) bool {
	if postID == "" {
		return false
	}
	// A running Gorillas game rewrites its world post ~30 times a second, and the
	// joiner's client would otherwise persist every one of those frames. Achtung,
	// die Kurve does the same with its own world post.
	if m.gorillasPost(postID) || m.kurvePost(postID) {
		return true
	}
	if m.typing.active && postID == m.typing.postID {
		return true
	}
	return false
}
