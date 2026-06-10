package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"matterbox/internal/auth"
	"matterbox/internal/config"
	"matterbox/internal/mm"
)

// mmauthRedirect is the redirect_to handed to Mattermost's mobile-login
// endpoint. "mmauth://" is in the server's default AppCustomURLSchemes, so
// the endpoint accepts it with no server-side configuration. After SSO the
// server bounces the browser to mmauth://callback?MMAUTHTOKEN=…, which we
// capture either via the OS scheme handler (Linux) or by the user pasting
// the link from the success page.
const mmauthRedirect = "mmauth://callback"

func newLoginCmd() *cobra.Command {
	var show, clear, noBrowser bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in via GitLab SSO and save the session token",
		Long: "Sign in to Mattermost using GitLab SSO and save the session token to\n" +
			"~/.config/matterbox/mm_token.json (where every other command reads it).\n\n" +
			"It opens your browser to the server's native-login endpoint; after you\n" +
			"authorize, the server hands the token back via an mmauth:// link. On Linux\n" +
			"matterbox registers itself as the mmauth:// handler so the token is captured\n" +
			"automatically; otherwise right-click the link on the success page, choose\n" +
			"\"Copy Link Address\", and paste it at the prompt (a raw token works too).\n\n" +
			"  matterbox login            # sign in and save the token\n" +
			"  matterbox login --show     # show the saved token's path and fingerprint\n" +
			"  matterbox login --clear    # delete the saved token",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case show:
				return runLoginShow(cmd.OutOrStdout())
			case clear:
				return runLoginClear(cmd.OutOrStdout())
			default:
				return runLogin(cmd.Context(), cmd.OutOrStdout(), noBrowser)
			}
		},
	}
	cmd.Flags().BoolVar(&show, "show", false, "print the saved token's path and a fingerprint, then exit")
	cmd.Flags().BoolVar(&clear, "clear", false, "delete the saved token, then exit")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "don't open a browser; just print the login URL")
	return cmd
}

// newURLHandlerCmd is the hidden command the OS invokes when the browser
// opens mmauth://callback?… after SSO (registered on Linux as an
// x-scheme-handler .desktop entry). It forwards the URL to the waiting
// `login` process over a local socket; with no login in progress it exits
// quietly. It is a no-op on platforms without the scheme-handler hook.
func newURLHandlerCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "url-handler [url]",
		Hidden: true,
		Args:   cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runURLHandler(args)
		},
	}
}

// newRegisterHandlerCmd registers this binary as the OS handler for the
// mmauth:// redirect that ends the SSO flow, so `matterbox login` captures
// the token without a copy-paste. `make install` runs it; `login` also does
// it lazily on each run. Linux-only (no-op elsewhere); hidden because it's
// install plumbing, not a daily verb.
func newRegisterHandlerCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "register-handler",
		Short:  "Register this binary as the mmauth:// login handler (Linux)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return registerSchemeHandler(cmd.OutOrStdout())
		},
	}
}

func runLogin(ctx context.Context, out io.Writer, noBrowser bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	server := strings.TrimRight(cfg.ServerURL, "/")
	if server == "" || server == config.PlaceholderServerURL {
		p, _ := config.Path()
		return fmt.Errorf("server_url is not set — edit %s and set server_url to your "+
			"Mattermost server, then re-run `matterbox login`", p)
	}

	loginURL := server + "/oauth/gitlab/mobile_login?redirect_to=" + url.QueryEscape(mmauthRedirect)

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	// tokenCh receives the first of: a URL captured by the OS scheme
	// handler (Linux), or a line pasted on stdin. Buffered so neither
	// producer blocks if the other wins the race.
	tokenCh := make(chan string, 2)

	// Linux: register the mmauth:// handler and listen on a local socket so
	// the token is captured automatically. No-op (enabled=false) elsewhere.
	cleanup, autoCapture := startURLHandlerCapture(ctx, tokenCh)
	defer cleanup()

	go readPasteInto(ctx, os.Stdin, tokenCh)

	switch {
	case noBrowser:
		fmt.Fprintln(out, "Open this URL to sign in with GitLab SSO:")
	case openURL(loginURL) != nil:
		fmt.Fprintln(out, "Couldn't open a browser automatically. Open this URL to sign in:")
	default:
		fmt.Fprintln(out, "Opening your browser to sign in with GitLab SSO…")
		fmt.Fprintln(out, "If it doesn't open, use this URL:")
	}
	fmt.Fprintf(out, "  %s\n\n", loginURL)

	if autoCapture {
		fmt.Fprintln(out, "After you authorize, your browser will offer to open the")
		fmt.Fprintln(out, "\"Matterbox Login Handler\" — allow it and you're done.")
		fmt.Fprintln(out, "If nothing happens, right-click the \"link\" on the success page,")
		fmt.Fprintln(out, "choose \"Copy Link Address\", and paste it below.")
	} else {
		fmt.Fprintln(out, "After you authorize, the success page shows a \"link\".")
		fmt.Fprintln(out, "Right-click it, choose \"Copy Link Address\", and paste it below.")
	}
	fmt.Fprint(out, "\nWaiting for sign-in… (or paste the link here and press Enter)\n› ")

	var raw string
	select {
	case raw = <-tokenCh:
	case <-ctx.Done():
		fmt.Fprintln(out)
		return ctx.Err()
	}

	token := extractToken(raw)
	if token == "" {
		return errors.New("couldn't find a token in that — expected an mmauth:// link or a raw session token")
	}
	return saveAndVerify(ctx, out, server, token)
}

// saveAndVerify checks the token against the server before persisting it,
// so a typo or expired link never overwrites a working token on disk.
func saveAndVerify(ctx context.Context, out io.Writer, server, token string) error {
	me, err := mm.New(server, token).Me(ctx)
	if err != nil {
		return fmt.Errorf("token didn't authenticate against %s: %w", server, err)
	}
	if err := auth.SaveToken(token); err != nil {
		return err
	}
	p, _ := auth.TokenPath()
	fmt.Fprintf(out, "\n✓ Logged in as %s (%s)\n", me.Username, me.Email)
	fmt.Fprintf(out, "  Token saved to %s\n", p)
	return nil
}

func runLoginShow(out io.Writer) error {
	p, err := auth.TokenPath()
	if err != nil {
		return err
	}
	tok, err := auth.ReadToken()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "token  %s\n", fingerprint(tok))
	fmt.Fprintf(out, "path   %s\n", p)
	return nil
}

func runLoginClear(out io.Writer) error {
	if err := auth.ClearToken(); err != nil {
		return err
	}
	fmt.Fprintln(out, "Saved token cleared.")
	return nil
}

// fingerprint abbreviates a token for display so `login --show` never
// prints the full credential to the terminal/scrollback.
func fingerprint(tok string) string {
	if len(tok) <= 12 {
		return strings.Repeat("•", len(tok))
	}
	return tok[:6] + "…" + tok[len(tok)-4:]
}

// readPasteInto reads one non-empty line from r and sends it to ch. Blank
// lines (a stray Enter while waiting on the handler) are skipped so they
// don't deliver an empty "token".
func readPasteInto(ctx context.Context, r io.Reader, ch chan<- string) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		select {
		case ch <- line:
		case <-ctx.Done():
		}
		return
	}
}

// extractToken pulls the session token out of whatever the user pasted: an
// mmauth://callback?MMAUTHTOKEN=… link, or a bare token. A URL without the
// MMAUTHTOKEN param (or any other multi-segment string) is rejected rather
// than mistaken for a token.
func extractToken(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	if s == "" {
		return ""
	}
	if u, err := url.Parse(s); err == nil {
		if t := strings.TrimSpace(u.Query().Get("MMAUTHTOKEN")); t != "" {
			return t
		}
	}
	// Not an mmauth:// link carrying the token → only accept it as a raw
	// token if it looks like one (no URL/whitespace punctuation).
	if strings.ContainsAny(s, " \t/?&#") {
		return ""
	}
	return s
}

// openURL launches the user's default browser at u. The command forks and
// returns immediately; we don't wait for the browser to exit.
func openURL(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start()
}
