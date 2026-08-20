package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"matterbox/internal/config"
	"matterbox/internal/forge/github"
	"matterbox/internal/githubauth"
	"matterbox/internal/opener"
)

func newGithubCmd() *cobra.Command {
	var show, clear, noBrowser bool
	var hostOverride string

	cmd := &cobra.Command{
		Use:   "github",
		Short: "GitHub issue / pull-request integration (reference panel)",
		Long: "GitHub auth for the issue/PR side panel mirrors GitLab: by default matterbox\n" +
			"reuses github.token, GITHUB_TOKEN/GH_TOKEN, or an existing `gh auth login`.\n" +
			"`matterbox github login` is an optional OAuth device-flow alternative when you\n" +
			"don't use the GitHub CLI.",
		Args: cobra.NoArgs,
	}

	var loginCmd = &cobra.Command{
		Use:   "login",
		Short: "Optional: sign in via OAuth device flow (when not using gh / a PAT)",
		Long: "Optional alternative to the default GitLab-style auth (PAT / GITHUB_TOKEN /\n" +
			"`gh auth login`). Runs GitHub OAuth device authorization and saves a token in\n" +
			"~/.config/matterbox/gh_token.json for this host.\n\n" +
			"Requires github.client_id in config.yaml (or GITHUB_CLIENT_ID).\n\n" +
			"Prefer `gh auth login` (or a PAT in config) when possible — the TUI picks those\n" +
			"up automatically, same as glab for GitLab.\n\n" +
			"Example:\n" +
			"  matterbox github login\n" +
			"  matterbox github login --hostname ghe.example.com",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGithubLogin(cmd.Context(), cmd.OutOrStdout(), noBrowser, hostOverride, show, clear)
		},
	}
	loginCmd.Flags().BoolVar(&noBrowser, "no-browser", false, "don't open the verification URL in a browser")

	var statusCmd = &cobra.Command{
		Use:   "status",
		Short: "Show which GitHub token the TUI would use for this host",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGithubStatus(cmd.Context(), cmd.OutOrStdout(), hostOverride)
		},
	}

	var logoutCmd = &cobra.Command{
		Use:   "logout",
		Short: "Delete the optional matterbox OAuth token for this host",
		Long:  "Clears only the token saved by `matterbox github login`. Does not touch `gh` or config/env tokens.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGithubLogout(cmd.Context(), cmd.OutOrStdout(), hostOverride)
		},
	}

	// shared flags
	loginCmd.Flags().BoolVar(&show, "show", false, "print the saved token's path and fingerprint")
	loginCmd.Flags().BoolVar(&clear, "clear", false, "delete the saved token, then exit")
	loginCmd.Flags().StringVar(&hostOverride, "hostname", "", "override the GitHub instance hostname (e.g. github.com, ghe.example.com)")

	// `status` and `logout` share the same override flag.
	statusCmd.Flags().StringVar(&hostOverride, "hostname", "", "override the GitHub instance hostname (e.g. github.com, ghe.example.com)")
	logoutCmd.Flags().StringVar(&hostOverride, "hostname", "", "override the GitHub instance hostname (e.g. github.com, ghe.example.com)")

	cmd.AddCommand(loginCmd, statusCmd, logoutCmd)
	_ = show  // used in runGithubLogin
	_ = clear // used in runGithubLogin
	return cmd
}

func runGithubStatus(ctx context.Context, out io.Writer, hostOverride string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	webBase := strings.TrimSpace(cfg.GitHub.BaseURL)
	if webBase == "" {
		webBase = "https://github.com"
	}
	if hostOverride != "" {
		u, uerr := parseURLFromHost(webBase, hostOverride)
		if uerr != nil {
			return uerr
		}
		webBase = u
	}
	host := githubauth.HostFromURL(webBase)
	if host == "" {
		return errors.New("github: couldn't determine host from github.base_url")
	}

	token, source := github.ResolveToken(cfg.GitHub.Token, host)
	if token == "" {
		fmt.Fprintf(out, "No GitHub token for %s\n", host)
		fmt.Fprintf(out, "  Set github.token, export GITHUB_TOKEN/GH_TOKEN, run `gh auth login`,\n")
		fmt.Fprintf(out, "  or use optional `matterbox github login` (needs github.client_id).\n")
		return nil
	}

	apiBase, err := githubauth.APIBaseFromWebBase(webBase)
	if err != nil {
		return err
	}
	httpClient := &http.Client{Timeout: 20 * time.Second}
	verifiedLogin, verr := githubauth.VerifyAccessToken(ctx, httpClient, apiBase, token)
	if verr != nil {
		fmt.Fprintf(out, "Token for %s (%s) doesn't verify: %v\n", host, source, verr)
		return nil
	}
	fmt.Fprintf(out, "GitHub connected for %s: %s (via %s)\n", host, verifiedLogin, source)
	return nil
}

func runGithubLogout(ctx context.Context, out io.Writer, hostOverride string) error {
	_ = ctx
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	webBase := strings.TrimSpace(cfg.GitHub.BaseURL)
	if webBase == "" {
		webBase = "https://github.com"
	}
	if hostOverride != "" {
		u, uerr := parseURLFromHost(webBase, hostOverride)
		if uerr != nil {
			return uerr
		}
		webBase = u
	}
	host := githubauth.HostFromURL(webBase)
	if host == "" {
		return errors.New("github: couldn't determine host from github.base_url")
	}

	if err := githubauth.ClearTokenForHost(host); err != nil {
		return err
	}
	fmt.Fprintf(out, "Matterbox OAuth token cleared for %s\n", host)
	fmt.Fprintf(out, "(config / env / gh CLI tokens are unchanged)\n")
	return nil
}

// parseURLFromHost returns a webBase where only the host changes.
func parseURLFromHost(webBase, hostOverride string) (string, error) {
	webBase = strings.TrimSpace(webBase)
	if webBase == "" {
		return "", errors.New("github: empty base_url")
	}
	hostOverride = strings.TrimSpace(hostOverride)
	if hostOverride == "" {
		return "", errors.New("github: empty hostname override")
	}
	u, err := url.Parse(webBase)
	if err != nil {
		return "", fmt.Errorf("github: parse base_url: %w", err)
	}
	if u.Scheme == "" {
		return "", errors.New("github: base_url missing scheme (expected https://host)")
	}
	u.Host = hostOverride
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

func runGithubLogin(ctx context.Context, out io.Writer, noBrowser bool, hostOverride string, show, clear bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	webBase := strings.TrimSpace(cfg.GitHub.BaseURL)
	if webBase == "" {
		webBase = "https://github.com"
	}
	// When an explicit hostname is provided, keep scheme from base_url and
	// overwrite host; better than forcing users to edit config just to login.
	if hostOverride != "" {
		u, uerr := parseURLFromHost(webBase, hostOverride)
		if uerr != nil {
			return uerr
		}
		webBase = u
	}

	host := githubauth.HostFromURL(webBase)
	if host == "" {
		return errors.New("github: couldn't determine host from github.base_url")
	}

	p, _ := githubauth.TokenPath()

	if clear {
		if err := githubauth.ClearTokenForHost(host); err != nil {
			return err
		}
		fmt.Fprintf(out, "GitHub token cleared for %s\n", host)
		return nil
	}

	if show {
		token, user, err := githubauth.ReadTokenForHost(host)
		if err != nil || token == "" {
			fmt.Fprintf(out, "No GitHub token saved for %s\n", host)
			return nil
		}
		if user != "" {
			fmt.Fprintf(out, "token  %s\n", fingerprint(token))
			fmt.Fprintf(out, "path   %s\n", p)
			fmt.Fprintf(out, "user   %s\n", user)
			return nil
		}
		fmt.Fprintf(out, "token  %s\n", fingerprint(token))
		fmt.Fprintf(out, "path   %s\n", p)
		return nil
	}

	clientID := strings.TrimSpace(cfg.GitHub.ClientID)
	if clientID == "" {
		if env := os.Getenv("GITHUB_CLIENT_ID"); env != "" {
			clientID = strings.TrimSpace(env)
		}
	}
	if clientID == "" {
		return errors.New("github login needs github.client_id in config.yaml (or GITHUB_CLIENT_ID env var)")
	}

	scopes := cfg.GitHub.Scopes
	if len(scopes) == 0 {
		scopes = []string{"repo"}
	}

	httpClient := &http.Client{Timeout: 20 * time.Second}

	fmt.Fprintf(out, "Starting GitHub device flow for %s…\n", host)
	start, err := githubauth.StartDeviceFlow(ctx, httpClient, webBase, clientID, scopes)
	if err != nil {
		return err
	}

	if start.UserCode != "" {
		fmt.Fprintf(out, "Enter this code in your browser: %s\n", start.UserCode)
	}
	if start.VerificationURI != "" {
		fmt.Fprintf(out, "URL: %s\n", start.VerificationURI)
	}

	if start.VerificationURIComplete != "" && !noBrowser {
		if opener.Open(start.VerificationURIComplete) != nil {
			fmt.Fprintln(out, "Couldn't open a browser automatically; use the URL above.")
		} else {
			fmt.Fprintln(out, "Opening your browser…")
		}
	}

	fmt.Fprintln(out, "Waiting for authorization…")
	token, err := githubauth.PollDeviceFlow(ctx, httpClient, webBase, clientID, start)
	if err != nil {
		return err
	}

	apiBase, err := githubauth.APIBaseFromWebBase(webBase)
	if err != nil {
		return err
	}

	login, err := githubauth.VerifyAccessToken(ctx, httpClient, apiBase, token)
	if err != nil {
		return fmt.Errorf("verify token: %w", err)
	}

	if err := githubauth.SaveTokenForHost(host, token, login); err != nil {
		return err
	}

	fmt.Fprintf(out, "\n✓ Signed in as %s (%s)\n", login, host)
	fmt.Fprintf(out, "  Token saved to %s\n", p)
	return nil
}
