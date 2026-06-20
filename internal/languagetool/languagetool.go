// Package languagetool is a thin client for a LanguageTool HTTP server's
// grammar/spell-check API (https://languagetool.org/http-api/). The composer
// uses it to underline mistakes in a draft and offer corrections; see
// internal/ui/grammar.go.
package languagetool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxReplacements caps how many suggestions we keep per match — the server can
// return dozens for an unknown word (the spelling example returns ~70), but the
// composer popup only ever shows a single-digit accelerator list.
const maxReplacements = 9

// Match is one finding: a span of the checked text plus its explanation and
// suggested fixes. Offset/Length are in characters (UTF-16 code units, as the
// server counts them) from the start of the checked text.
type Match struct {
	Offset       int
	Length       int
	Message      string   // full human explanation
	Short        string   // terse label ("Spelling mistake"); may be empty
	Replacements []string // suggested corrections, best first
	IssueType    string   // rule.issueType: "misspelling", "grammar", "style", …
	Category     string   // rule.category.id: "TYPOS", "GRAMMAR", …
	RuleID       string   // rule.id, for debugging/allowlisting later
}

// Client talks to one LanguageTool server. Safe for concurrent use.
type Client struct {
	checkURL string
	language string
	http     *http.Client
}

// New builds a client for the given server. serverURL is the API /v2 root (e.g.
// http://localhost:8010/v2); the check endpoint is that + /check. language is a
// code like "en-US" or "auto". timeout bounds a single request (0 picks a
// sensible default).
func New(serverURL, language string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	base := strings.TrimRight(serverURL, "/")
	return &Client{
		checkURL: base + "/check",
		language: language,
		http:     &http.Client{Timeout: timeout},
	}
}

// ltResponse mirrors the subset of the /check JSON we use.
type ltResponse struct {
	Matches []struct {
		Message      string `json:"message"`
		ShortMessage string `json:"shortMessage"`
		Offset       int    `json:"offset"`
		Length       int    `json:"length"`
		Replacements []struct {
			Value string `json:"value"`
		} `json:"replacements"`
		Rule struct {
			ID        string `json:"id"`
			IssueType string `json:"issueType"`
			Category  struct {
				ID string `json:"id"`
			} `json:"category"`
		} `json:"rule"`
	} `json:"matches"`
}

// Check returns the findings for text. A nil error with an empty slice means
// the server found nothing wrong. The context bounds the call (the client also
// has its own timeout).
func (c *Client) Check(ctx context.Context, text string) ([]Match, error) {
	form := url.Values{}
	form.Set("text", text)
	form.Set("language", c.language)
	// level=default keeps it to high-confidence rules; the picky extras are
	// noisy in casual chat.
	form.Set("level", "default")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.checkURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("languagetool: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var lr ltResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return nil, fmt.Errorf("languagetool: decode response: %w", err)
	}

	matches := make([]Match, 0, len(lr.Matches))
	for _, mm := range lr.Matches {
		reps := make([]string, 0, maxReplacements)
		for _, r := range mm.Replacements {
			if r.Value == "" {
				continue
			}
			reps = append(reps, r.Value)
			if len(reps) == maxReplacements {
				break
			}
		}
		matches = append(matches, Match{
			Offset:       mm.Offset,
			Length:       mm.Length,
			Message:      mm.Message,
			Short:        mm.ShortMessage,
			Replacements: reps,
			IssueType:    mm.Rule.IssueType,
			Category:     mm.Rule.Category.ID,
			RuleID:       mm.Rule.ID,
		})
	}
	return matches, nil
}
