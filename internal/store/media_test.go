package store

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// mkFilePost builds a post carrying the named files as resolved FileInfo
// metadata, the shape the cache stores once attachments are backfilled.
func mkFilePost(id, channelID string, createAt int64, names ...string) *model.Post {
	p := mkPost(id, channelID, "", createAt)
	files := make([]*model.FileInfo, 0, len(names))
	for i, name := range names {
		files = append(files, &model.FileInfo{
			Id:          name + "-id",
			PostId:      id,
			CreatorId:   "u1",
			CreateAt:    createAt,
			Name:        name,
			Size:        int64(100 + i),
			MimeType:    "image/png",
			MiniPreview: &[]byte{1, 2, 3},
		})
		p.FileIds = append(p.FileIds, name+"-id")
	}
	p.Metadata = &model.PostMetadata{Files: files}
	return p
}

func fileNames(files []*model.FileInfo) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Name
	}
	return out
}

func TestChannelFilesNewestFirst(t *testing.T) {
	s := tempStore(t)
	posts := []*model.Post{
		mkFilePost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", 100, "old.png"),
		mkFilePost("p2aaaaaaaaaaaaaaaaaaaaaaaa", "c1", 300, "new.png"),
		mkFilePost("p3aaaaaaaaaaaaaaaaaaaaaaaa", "c1", 200, "mid.png"),
		mkPost("p4aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "no attachments", 400),
		mkFilePost("p5aaaaaaaaaaaaaaaaaaaaaaaa", "c2", 500, "other-channel.png"),
	}
	if err := s.UpsertMany(posts); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.ChannelFiles("c1", 100)
	if err != nil {
		t.Fatalf("channel files: %v", err)
	}
	want := []string{"new.png", "mid.png", "old.png"}
	if diff := fileNames(got); !equalStrings(diff, want) {
		t.Errorf("order: got %v, want %v", diff, want)
	}
}

// A post's own attachments keep their upload order; only posts are sorted.
func TestChannelFilesMultiFilePostKeepsAttachmentOrder(t *testing.T) {
	s := tempStore(t)
	if err := s.UpsertMany([]*model.Post{
		mkFilePost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", 100, "a.png", "b.png", "c.png"),
		mkFilePost("p2aaaaaaaaaaaaaaaaaaaaaaaa", "c1", 200, "z.png"),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.ChannelFiles("c1", 100)
	if err != nil {
		t.Fatalf("channel files: %v", err)
	}
	want := []string{"z.png", "a.png", "b.png", "c.png"}
	if names := fileNames(got); !equalStrings(names, want) {
		t.Errorf("got %v, want %v", names, want)
	}
}

// limit caps posts scanned, not files returned — a limit of 1 still yields
// both attachments of the newest post.
func TestChannelFilesLimitIsPerPost(t *testing.T) {
	s := tempStore(t)
	if err := s.UpsertMany([]*model.Post{
		mkFilePost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", 100, "old.png"),
		mkFilePost("p2aaaaaaaaaaaaaaaaaaaaaaaa", "c1", 200, "new1.png", "new2.png"),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.ChannelFiles("c1", 1)
	if err != nil {
		t.Fatalf("channel files: %v", err)
	}
	want := []string{"new1.png", "new2.png"}
	if names := fileNames(got); !equalStrings(names, want) {
		t.Errorf("got %v, want %v", names, want)
	}
}

func TestChannelFilesExcludesDeleted(t *testing.T) {
	s := tempStore(t)
	gone := mkFilePost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", 200, "gone.png")
	kept := mkFilePost("p2aaaaaaaaaaaaaaaaaaaaaaaa", "c1", 100, "kept.png")
	if err := s.UpsertMany([]*model.Post{gone, kept}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.Delete(gone); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, err := s.ChannelFiles("c1", 100)
	if err != nil {
		t.Fatalf("channel files: %v", err)
	}
	if names := fileNames(got); !equalStrings(names, []string{"kept.png"}) {
		t.Errorf("deleted post's file leaked: %v", names)
	}
}

// The base64 thumbnail is stripped in SQL so it never costs a decode.
func TestChannelFilesDropsMiniPreview(t *testing.T) {
	s := tempStore(t)
	if err := s.Upsert(mkFilePost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", 100, "a.png")); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.ChannelFiles("c1", 100)
	if err != nil {
		t.Fatalf("channel files: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 file, got %d", len(got))
	}
	if got[0].MiniPreview != nil {
		t.Errorf("mini_preview should be stripped, got %v", *got[0].MiniPreview)
	}
	if got[0].Size != 100 || got[0].MimeType != "image/png" || got[0].PostId == "" {
		t.Errorf("file fields lost: %+v", got[0])
	}
}

func TestChannelFilesEmptyAndGuards(t *testing.T) {
	s := tempStore(t)
	if err := s.Upsert(mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "text only", 100)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	for _, tc := range []struct {
		name      string
		channelID string
		limit     int
	}{
		{"no files in channel", "c1", 100},
		{"unknown channel", "nope", 100},
		{"empty channel id", "", 100},
		{"zero limit", "c1", 0},
		{"negative limit", "c1", -1},
	} {
		got, err := s.ChannelFiles(tc.channelID, tc.limit)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
		if len(got) != 0 {
			t.Errorf("%s: want no files, got %d", tc.name, len(got))
		}
	}

	var nilStore *Store
	if got, err := nilStore.ChannelFiles("c1", 10); err != nil || got != nil {
		t.Errorf("nil store: got %v, %v", got, err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
