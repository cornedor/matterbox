package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGiphyURLID(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantID   string
		wantSlug string
		wantOK   bool
	}{
		{
			name:     "share page with slug",
			raw:      "https://giphy.com/gifs/good-morning-wake-up-foQn7E3SfhnxXuB0LY",
			wantID:   "foQn7E3SfhnxXuB0LY",
			wantSlug: "good-morning-wake-up",
			wantOK:   true,
		},
		{
			name:   "share page bare id",
			raw:    "https://giphy.com/gifs/foQn7E3SfhnxXuB0LY",
			wantID: "foQn7E3SfhnxXuB0LY",
			wantOK: true,
		},
		{
			name:   "copy-link media url",
			raw:    "https://media3.giphy.com/media/v1.Y2lkPTc5MGI3NjExMmtqano2ZTk0aGhqaHl0OTdxZW8zdWEzcWl6bjM2a3IzaDZhZ2lwbCZlcD12MV9pbnRlcm5hbF9naWZfYnlfaWQmY3Q9Zw/foQn7E3SfhnxXuB0LY/giphy.gif",
			wantID: "foQn7E3SfhnxXuB0LY",
			wantOK: true,
		},
		{
			name:   "short media url no cache id",
			raw:    "https://media.giphy.com/media/foQn7E3SfhnxXuB0LY/200.gif",
			wantID: "foQn7E3SfhnxXuB0LY",
			wantOK: true,
		},
		{
			name:   "i.giphy.com short embed",
			raw:    "https://i.giphy.com/foQn7E3SfhnxXuB0LY.gif",
			wantID: "foQn7E3SfhnxXuB0LY",
			wantOK: true,
		},
		{
			name:     "stickers path",
			raw:      "https://giphy.com/stickers/cat-fun-GRk3GLfzduq1NtfGt5",
			wantID:   "GRk3GLfzduq1NtfGt5",
			wantSlug: "cat-fun",
			wantOK:   true,
		},
		{name: "non-giphy host", raw: "https://example.com/gifs/foo-bar123", wantOK: false},
		{name: "lookalike host", raw: "https://giphy.com.evil.com/gifs/x-foQn7E3SfhnxXuB0LY", wantOK: false},
		{name: "no scheme", raw: "giphy.com/gifs/foo-foQn7E3SfhnxXuB0LY", wantOK: false},
		{name: "empty", raw: "", wantOK: false},
		{name: "homepage", raw: "https://giphy.com/", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, slug, ok := giphyURLID(tc.raw)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (id=%q slug=%q)", ok, tc.wantOK, id, slug)
			}
			if !tc.wantOK {
				return
			}
			if id != tc.wantID {
				t.Errorf("id = %q, want %q", id, tc.wantID)
			}
			if slug != tc.wantSlug {
				t.Errorf("slug = %q, want %q", slug, tc.wantSlug)
			}
		})
	}
}

func TestGiphyExpand(t *testing.T) {
	// Page URL with a slug → readable alt + the configured rendition file.
	md, id, ok := giphyExpand("https://giphy.com/gifs/good-morning-wake-up-foQn7E3SfhnxXuB0LY", "fixed_height")
	if !ok {
		t.Fatal("expected a Giphy URL to expand")
	}
	if id != "foQn7E3SfhnxXuB0LY" {
		t.Errorf("id = %q", id)
	}
	want := "![good morning wake up](https://media.giphy.com/media/foQn7E3SfhnxXuB0LY/200.gif)"
	if md != want {
		t.Errorf("markdown =\n  %q\nwant\n  %q", md, want)
	}

	// Copy-link media URL carries no slug → generic "gif" alt, original rendition.
	md, _, ok = giphyExpand("https://media3.giphy.com/media/v1.abc/foQn7E3SfhnxXuB0LY/giphy.gif", "original")
	if !ok {
		t.Fatal("expected a media URL to expand")
	}
	want = "![gif](https://media.giphy.com/media/foQn7E3SfhnxXuB0LY/giphy.gif)"
	if md != want {
		t.Errorf("markdown =\n  %q\nwant\n  %q", md, want)
	}

	// A non-Giphy URL is left for the caller to paste through unchanged.
	if _, _, ok := giphyExpand("https://example.com/cat.gif", "fixed_height"); ok {
		t.Error("non-Giphy URL should not expand")
	}
}

func TestGiphyRenditionFile(t *testing.T) {
	cases := map[string]string{
		"fixed_height":             "200.gif",
		"fixed_height_small":       "100.gif",
		"fixed_height_downsampled": "200_d.gif",
		"fixed_width":              "200w.gif",
		"original":                 "giphy.gif",
		"downsized":                "giphy.gif", // not served off the bare host
		"":                         "giphy.gif",
	}
	for rendition, want := range cases {
		if got := giphyRenditionFile(rendition); got != want {
			t.Errorf("giphyRenditionFile(%q) = %q, want %q", rendition, got, want)
		}
	}
}

func TestGiphyLookup(t *testing.T) {
	const body = `{
		"data": {
			"title": "Sleepy Good Morning GIF by Fresh Cake",
			"slug": "good-morning-wake-up-foQn7E3SfhnxXuB0LY",
			"images": {
				"original":     {"url": "https://media.example/orig/giphy.gif"},
				"fixed_height": {"url": "https://media.example/fh/200.gif"}
			}
		},
		"meta": {"status": 200, "msg": "OK"}
	}`
	var gotPath, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.URL.Query().Get("api_key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	oldBase := giphyAPIBase
	giphyAPIBase = srv.URL + "/v1/gifs/"
	defer func() { giphyAPIBase = oldBase }()

	old := "![good morning wake up](https://media.giphy.com/media/foQn7E3SfhnxXuB0LY/200.gif)"
	msg := giphyLookup(context.Background(), "secretkey", "foQn7E3SfhnxXuB0LY", "fixed_height", old)()
	res, ok := msg.(giphyResolvedMsg)
	if !ok {
		t.Fatalf("expected giphyResolvedMsg, got %T", msg)
	}
	if res.err != nil {
		t.Fatalf("unexpected error: %v", res.err)
	}
	want := "![Sleepy Good Morning GIF by Fresh Cake](https://media.example/fh/200.gif)"
	if res.markdown != want {
		t.Errorf("markdown = %q, want %q", res.markdown, want)
	}
	if res.old != old {
		t.Errorf("old = %q, want %q", res.old, old)
	}
	if gotPath != "/v1/gifs/foQn7E3SfhnxXuB0LY" {
		t.Errorf("request path = %q", gotPath)
	}
	if gotKey != "secretkey" {
		t.Errorf("api_key = %q", gotKey)
	}
}

func TestGiphyLookupFallsBackToOriginal(t *testing.T) {
	// The requested rendition is absent → lookup falls back to original.
	const body = `{"data":{"title":"X","images":{"original":{"url":"https://media.example/orig/giphy.gif"}}},"meta":{"status":200}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	oldBase := giphyAPIBase
	giphyAPIBase = srv.URL + "/v1/gifs/"
	defer func() { giphyAPIBase = oldBase }()

	msg := giphyLookup(context.Background(), "k", "abcde", "downsized", "OLD")()
	res := msg.(giphyResolvedMsg)
	if res.err != nil {
		t.Fatalf("unexpected error: %v", res.err)
	}
	want := "![X](https://media.example/orig/giphy.gif)"
	if res.markdown != want {
		t.Errorf("markdown = %q, want %q", res.markdown, want)
	}
}

func TestGiphyLookupHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	oldBase := giphyAPIBase
	giphyAPIBase = srv.URL + "/v1/gifs/"
	defer func() { giphyAPIBase = oldBase }()

	msg := giphyLookup(context.Background(), "k", "abcde", "fixed_height", "OLD")()
	res := msg.(giphyResolvedMsg)
	if res.err == nil {
		t.Fatal("expected an error for a 403 response")
	}
	if res.markdown != "" {
		t.Errorf("markdown should be empty on error, got %q", res.markdown)
	}
	if res.old != "OLD" {
		t.Errorf("old = %q, want OLD (so the instant expansion is kept)", res.old)
	}
}
