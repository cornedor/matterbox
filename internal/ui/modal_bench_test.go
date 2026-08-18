package ui

import (
	"strconv"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/viewport"
)

// BenchmarkRenderListModal is one frame of a list sheet: the saved-messages
// browser at its 200-item page (rows labelled through findChannel /
// postAuthorName) and the kaomoji picker.
func BenchmarkRenderListModal(b *testing.B) {
	m := benchVisibleModel(200)
	m.width, m.height = 160, 48
	m.me = &model.User{Id: "me"}
	m.openSavedPosts()
	var page []*model.Post
	for i := 0; i < 200; i++ {
		page = append(page, &model.Post{
			Id: "saved" + strconv.Itoa(i), ChannelId: "chan" + strconv.Itoa(i), UserId: "u" + strconv.Itoa(i%7),
			Message: "a saved message body number " + strconv.Itoa(i) + " with some words in it",
		})
	}
	loaded, _ := m.applySavedPostsLoaded(savedPostsLoadedMsg{gen: m.savedPosts.gen, items: page})
	m = loaded.(Model)
	m.savedPosts.idx = 150
	b.Run("saved/items=200", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = m.renderSavedPosts()
		}
	})
	m.openKaomojiPicker()
	b.Run("kaomoji", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = m.renderKaomojiPicker()
		}
	})
	ks := viewport.New()
	ks.SoftWrap = true
	m.keysSheetView = &ks
	m.keys = newKeyMap("ctrl")
	m.openKeysSheet()
	b.Run("keys-sheet", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = m.renderKeysSheetPopup()
		}
	})
}
