package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Pasting a Giphy link drops an inline image into the composer. A Giphy URL —
// either a share page (giphy.com/gifs/<slug>-<id>) or a "Copy link" media URL
// (media*.giphy.com/media/.../<id>/giphy.gif) — carries the GIF id, which is
// all we need to build a canonical ![alt](url) image. That expansion happens
// instantly and offline in handlePaste; if a Giphy API key is configured, a
// background lookup then upgrades the line in place with the GIF's real title
// and the configured rendition (giphyLookup → giphyResolvedMsg). The body-image
// preview (see preview.go / collectOpenables) renders the result like any other
// inline image, so it posts exactly as the Mattermost GIF picker's links do.

// giphyHTTPClient fetches GIF metadata from the Giphy API. The timeout keeps a
// slow host from leaving the composer line un-upgraded for long; on a trip the
// instant (offline) expansion already in the composer simply stands.
var giphyHTTPClient = &http.Client{Timeout: 10 * time.Second}

// giphyAPIBase is the Giphy "get GIF by id" endpoint; the id is appended. A var
// (not const) so tests can point it at a local server.
var giphyAPIBase = "https://api.giphy.com/v1/gifs/"

// giphyResolvedMsg carries the result of a background API lookup. old is the
// instant markdown the composer already holds; markdown is the upgraded line to
// swap in (real title + configured rendition). A swap only happens when old is
// still present in the input, so editing in the meantime is never clobbered.
type giphyResolvedMsg struct {
	old      string
	markdown string
	err      error
}

// giphyURLID parses a Giphy share or media URL and returns the GIF id, plus the
// page slug when present (a "Copy link" media URL carries no slug). ok is false
// for non-Giphy hosts or URLs without a recognisable id.
func giphyURLID(raw string) (id, slug string, ok bool) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", false
	}
	host := strings.ToLower(u.Hostname())
	if host != "giphy.com" && !strings.HasSuffix(host, ".giphy.com") {
		return "", "", false
	}
	var segs []string
	for _, s := range strings.Split(u.Path, "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	if len(segs) == 0 {
		return "", "", false
	}
	switch segs[0] {
	case "gifs", "stickers", "clips":
		// /gifs/<slug>-<id> (or just /gifs/<id>). The id is the last
		// "-"-delimited token of the first segment after the kind.
		id, slug = giphySplitSlug(segs[1])
	case "media":
		// /media/<id>/giphy.gif  or  /media/v1.<cacheid>/<id>/<file>. The id is
		// the path segment just before the rendition filename.
		switch len(segs) {
		case 2:
			id = segs[1]
		default:
			id = segs[len(segs)-2]
		}
	default:
		// i.giphy.com/<id>.gif (the short embed host): id is the filename stem.
		// Require a file extension so a non-GIF path (e.g. /categories) isn't
		// mistaken for an id.
		last := segs[len(segs)-1]
		if ext := path.Ext(last); ext != "" {
			id = strings.TrimSuffix(last, ext)
		}
	}
	if !giphyValidID(id) {
		return "", "", false
	}
	return id, slug, true
}

// giphySplitSlug splits a "<words>-<id>" page slug into the id (the final token)
// and the descriptive prefix. A bare id with no prefix yields an empty slug.
func giphySplitSlug(s string) (id, slug string) {
	if i := strings.LastIndex(s, "-"); i >= 0 {
		return s[i+1:], s[:i]
	}
	return s, ""
}

// giphyValidID reports whether s looks like a Giphy id: a run of ASCII
// alphanumerics of plausible length. Guards against treating a stray path
// segment (or a trailing word like "fullscreen") as an id.
func giphyValidID(s string) bool {
	if len(s) < 5 || len(s) > 40 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// giphyRenditionFile maps a rendition name to the bare-host filename that
// serves it (media.giphy.com/media/<id>/<file>). The downsized family isn't
// served off the bare host, so it falls back to the original — the API lookup,
// when keyed, supplies the true downsized URL.
func giphyRenditionFile(rendition string) string {
	switch rendition {
	case "fixed_height":
		return "200.gif"
	case "fixed_height_small":
		return "100.gif"
	case "fixed_height_downsampled":
		return "200_d.gif"
	case "fixed_width":
		return "200w.gif"
	default: // original, downsized, downsized_medium, or anything unknown
		return "giphy.gif"
	}
}

// giphyAltText turns a page slug ("good-morning-wake-up") into readable alt text
// ("good morning wake up"). A slug-less media link falls back to "gif".
func giphyAltText(slug string) string {
	alt := strings.TrimSpace(strings.ReplaceAll(slug, "-", " "))
	if alt == "" {
		return "gif"
	}
	return alt
}

// giphySanitizeAlt makes a title safe to drop between the brackets of
// ![alt](url): markdown brackets and newlines would otherwise break the image.
var giphySanitizeAlt = strings.NewReplacer("[", "(", "]", ")", "\n", " ", "\r", " ").Replace

// giphyMarkdown builds an ![alt](url) image for a GIF id at the bare-host URL
// for the given rendition.
func giphyMarkdown(alt, id, rendition string) string {
	img := "https://media.giphy.com/media/" + id + "/" + giphyRenditionFile(rendition)
	return fmt.Sprintf("![%s](%s)", giphySanitizeAlt(alt), img)
}

// giphyExpand turns a pasted Giphy URL into instant inline-image markdown,
// offline. ok is false when raw isn't a recognisable Giphy URL — the caller
// then pastes it through unchanged. The returned id/markdown drive an optional
// background title upgrade (giphyLookup).
func giphyExpand(raw, rendition string) (markdown, id string, ok bool) {
	id, slug, ok := giphyURLID(raw)
	if !ok {
		return "", "", false
	}
	return giphyMarkdown(giphyAltText(slug), id, rendition), id, true
}

// giphyAPIResponse is the slice of the Giphy "GIF by id" payload we use.
type giphyAPIResponse struct {
	Data struct {
		Title  string `json:"title"`
		Slug   string `json:"slug"`
		Images map[string]struct {
			URL string `json:"url"`
		} `json:"images"`
	} `json:"data"`
	Meta struct {
		Status int    `json:"status"`
		Msg    string `json:"msg"`
	} `json:"meta"`
}

// giphyLookup fetches a GIF's real title and the configured rendition URL from
// the Giphy API, returning a giphyResolvedMsg that swaps the instant markdown
// (old) for the upgraded line. On any error it returns the error so the caller
// can keep the instant expansion and (quietly) note the failure.
func giphyLookup(ctx context.Context, apiKey, id, rendition, old string) tea.Cmd {
	return func() tea.Msg {
		endpoint := giphyAPIBase + url.PathEscape(id) + "?api_key=" + url.QueryEscape(apiKey)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return giphyResolvedMsg{old: old, err: err}
		}
		resp, err := giphyHTTPClient.Do(req)
		if err != nil {
			return giphyResolvedMsg{old: old, err: err}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return giphyResolvedMsg{old: old, err: fmt.Errorf("giphy api: %s", resp.Status)}
		}
		var out giphyAPIResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return giphyResolvedMsg{old: old, err: err}
		}
		img := out.Data.Images[rendition].URL
		if img == "" {
			img = out.Data.Images["original"].URL
		}
		if img == "" {
			// API gave us nothing usable; the instant bare-host URL stands.
			return giphyResolvedMsg{old: old, err: fmt.Errorf("giphy api: no rendition for %s", id)}
		}
		alt := giphySanitizeAlt(out.Data.Title)
		if strings.TrimSpace(alt) == "" {
			alt = giphyAltText(out.Data.Slug)
		}
		md := fmt.Sprintf("![%s](%s)", alt, img)
		if md == old {
			return giphyResolvedMsg{old: old, markdown: ""} // no change to apply
		}
		return giphyResolvedMsg{old: old, markdown: md}
	}
}
