package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"matterbox/internal/auth"
	"matterbox/internal/config"
	"matterbox/internal/mm"
	"matterbox/internal/mmauth"
	"matterbox/internal/opener"
)

func newLoginCmd() *cobra.Command {
	var show, clear, noBrowser, password bool
	var user string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in and save the session token (GitLab SSO or username/password)",
		Long: "Sign in to Mattermost and save the session token to\n" +
			"~/.config/matterbox/mm_token.json (where every other command reads it).\n\n" +
			"By default it signs in with GitLab SSO: it opens your browser to the\n" +
			"server's native-login endpoint; after you authorize, the server hands the\n" +
			"token back via an mmauth:// link. On Linux matterbox registers itself as the\n" +
			"mmauth:// handler so the token is captured automatically; otherwise right-click\n" +
			"the link on the success page, choose \"Copy Link Address\", and paste it at the\n" +
			"prompt (a raw token works too).\n\n" +
			"With --user/--password it signs in with a username (or email) and password\n" +
			"instead, prompting for whatever you don't pass; the password is read without\n" +
			"echo and never taken as a flag. If the server requires a two-factor code\n" +
			"you'll be prompted for it.\n\n" +
			"  matterbox login              # GitLab SSO (default)\n" +
			"  matterbox login --user me    # username/password (prompts for the password)\n" +
			"  matterbox login --password   # username/password (prompts for both)\n" +
			"  matterbox login --show       # show the saved token's path and fingerprint\n" +
			"  matterbox login --clear      # delete the saved token",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case show:
				return runLoginShow(cmd.OutOrStdout())
			case clear:
				return runLoginClear(cmd.OutOrStdout())
			case password || user != "":
				return runPasswordLogin(cmd.Context(), cmd.OutOrStdout(), user)
			default:
				return runLogin(cmd.Context(), cmd.OutOrStdout(), noBrowser)
			}
		},
	}
	cmd.Flags().BoolVar(&show, "show", false, "print the saved token's path and a fingerprint, then exit")
	cmd.Flags().BoolVar(&clear, "clear", false, "delete the saved token, then exit")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "don't open a browser; just print the login URL")
	cmd.Flags().BoolVar(&password, "password", false, "sign in with username/password instead of GitLab SSO (prompts for both)")
	cmd.Flags().StringVarP(&user, "user", "u", "", "username or email for password sign-in (implies --password; prompts if omitted)")
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
			if len(args) == 0 {
				return nil
			}
			return mmauth.HandleURL(args[0])
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
			p, err := mmauth.RegisterHandler()
			if err != nil {
				return err
			}
			if p == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "mmauth:// handler registration is only supported on Linux — skipping")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "registered mmauth:// login handler → %s\n", p)
			return nil
		},
	}
}

func runLogin(ctx context.Context, out io.Writer, noBrowser bool) error {
	server, err := resolveServer()
	if err != nil {
		return err
	}

	loginURL := mmauth.LoginURL(server)

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	// Linux: register the mmauth:// handler and listen on a local socket so the
	// token is captured automatically. No-op (enabled=false) elsewhere.
	cap, autoCapture := mmauth.StartCapture(ctx)
	defer cap.Close()

	// pasteCh receives a link/token pasted on stdin — the fallback (and the only
	// path off Linux). Buffered so the reader never blocks once a URL is captured.
	pasteCh := make(chan string, 1)
	go readPasteInto(ctx, os.Stdin, pasteCh)

	switch {
	case noBrowser:
		fmt.Fprintln(out, "Open this URL to sign in with GitLab SSO:")
	case opener.Open(loginURL) != nil:
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
	case raw = <-cap.URL: // nil channel when capture is disabled — never fires
	case raw = <-pasteCh:
	case <-ctx.Done():
		fmt.Fprintln(out)
		return ctx.Err()
	}

	token := mmauth.ExtractToken(raw)
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
	return saveToken(out, token, me.Username, me.Email)
}

// saveToken persists a session token and reports where it landed. Shared by the
// SSO/paste flow (after verifying the token with Me) and the username/password
// flow (which already holds the authenticated user from logging in).
func saveToken(out io.Writer, token, username, email string) error {
	if err := auth.SaveToken(token); err != nil {
		return err
	}
	p, _ := auth.TokenPath()
	fmt.Fprintf(out, "\n✓ Logged in as %s (%s)\n", username, email)
	fmt.Fprintf(out, "  Token saved to %s\n", p)
	return nil
}

// resolveServer loads the configured server URL (scheme kept, trailing slash
// trimmed), or returns a pointed error when it hasn't been set yet. Shared by
// every login flow.
func resolveServer() (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	server := strings.TrimRight(cfg.ServerURL, "/")
	if server == "" || server == config.PlaceholderServerURL {
		p, _ := config.Path()
		return "", fmt.Errorf("server_url is not set — edit %s and set server_url to your "+
			"Mattermost server, then re-run `matterbox login`", p)
	}
	return server, nil
}

// runPasswordLogin signs in with a username (or email) and password, prompting
// for whatever wasn't passed on the flags. The password is read without echo
// and is never accepted as a flag, so it can't leak into shell history. If the
// server demands a two-factor code the first attempt surfaces that and we
// prompt for the code and retry. On success the issued token is saved.
func runPasswordLogin(ctx context.Context, out io.Writer, loginID string) error {
	server, err := resolveServer()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	in := bufio.NewReader(os.Stdin)
	loginID = strings.TrimSpace(loginID)
	if loginID == "" {
		if loginID, err = promptLine(in, out, "Username or email: "); err != nil {
			return err
		}
		if loginID == "" {
			return errors.New("no username given")
		}
	}
	pass, err := readSecret(in, out, "Password: ")
	if err != nil {
		return err
	}
	if pass == "" {
		return errors.New("no password given")
	}

	client := mm.New(server, "")
	token, user, err := client.LoginWithPassword(ctx, loginID, pass, "")
	if mm.MFARequired(err) {
		code, perr := promptLine(in, out, "Two-factor code: ")
		if perr != nil {
			return perr
		}
		if code == "" {
			return errors.New("no two-factor code given")
		}
		token, user, err = client.LoginWithPassword(ctx, loginID, pass, code)
	}
	if err != nil {
		return fmt.Errorf("sign-in failed: %w", err)
	}
	return saveToken(out, token, user.Username, user.Email)
}

// promptLine writes prompt to out and reads one trimmed line from in. A final
// line without a trailing newline (EOF) still counts.
func promptLine(in *bufio.Reader, out io.Writer, prompt string) (string, error) {
	fmt.Fprint(out, prompt)
	line, err := in.ReadString('\n')
	line = strings.TrimSpace(line)
	if err != nil && !(err == io.EOF && line != "") {
		return "", err
	}
	return line, nil
}

// readSecret prompts and reads a line without echoing it, so a password never
// shows on screen or in scrollback. When stdin isn't a terminal (piped input)
// it falls back to a normal line read from the same buffered reader.
func readSecret(in *bufio.Reader, out io.Writer, prompt string) (string, error) {
	fmt.Fprint(out, prompt)
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(out) // the un-echoed Enter left the cursor on the prompt line
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	return promptLine(in, out, "") // prompt already written above
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

