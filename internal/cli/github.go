package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"matterbox/internal/config"
	"matterbox/internal/github"
	"matterbox/internal/githubauth"
)

func newGithubCmd() *cobra.Command {
	var hostOverride string

	cmd := &cobra.Command{
		Use:   "github",
		Short: "GitHub issue / pull-request integration (reference panel)",
		Long: "GitHub auth for the issue/PR side panel mirrors GitLab: use github.token,\n" +
			"GITHUB_TOKEN/GH_TOKEN, or an existing `gh auth login`. No separate OAuth App\n" +
			"registration is required.",
		Args: cobra.NoArgs,
	}

	var loginCmd = &cobra.Command{
		Use:   "login",
		Short: "How to authenticate (gh auth login or a PAT)",
		Long: "Prints how to authenticate for the GitHub panel. Prefer `gh auth login` or a\n" +
			"PAT in config/env — same pattern as GitLab's glab / GITLAB_TOKEN.\n\n" +
			"Example:\n" +
			"  gh auth login\n" +
			"  matterbox github status",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGithubLogin(cmd.OutOrStdout(), hostOverride)
		},
	}

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
		Short: "Delete a leftover matterbox-saved GitHub token for this host",
		Long:  "Clears only a token file under ~/.config/matterbox/. Does not touch `gh` or config/env tokens.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGithubLogout(cmd.Context(), cmd.OutOrStdout(), hostOverride)
		},
	}

	loginCmd.Flags().StringVar(&hostOverride, "hostname", "", "override the GitHub instance hostname (e.g. github.com, ghe.example.com)")
	statusCmd.Flags().StringVar(&hostOverride, "hostname", "", "override the GitHub instance hostname (e.g. github.com, ghe.example.com)")
	logoutCmd.Flags().StringVar(&hostOverride, "hostname", "", "override the GitHub instance hostname (e.g. github.com, ghe.example.com)")

	cmd.AddCommand(loginCmd, statusCmd, logoutCmd)
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
		fmt.Fprintf(out, "  Set github.token, export GITHUB_TOKEN/GH_TOKEN, or run `gh auth login`.\n")
		return nil
	}

	apiBase, err := githubauth.APIBaseFromWebBase(webBase)
	if err != nil {
		return err
	}
	httpClient := &http.Client{Timeout: 20 * time.Second}
	verifiedLogin, verr := githubauth.VerifyAccessToken(ctx, httpClient, apiBase, token)
	fmt.Fprintf(out, "host   %s\n", host)
	fmt.Fprintf(out, "source %s\n", source)
	fmt.Fprintf(out, "token  %s\n", fingerprint(token))
	if verr != nil {
		fmt.Fprintf(out, "verify failed: %v\n", verr)
		return nil
	}
	fmt.Fprintf(out, "user   %s\n", verifiedLogin)
	return nil
}

func runGithubLogout(_ context.Context, out io.Writer, hostOverride string) error {
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
	fmt.Fprintf(out, "Cleared matterbox-saved GitHub token for %s (if any)\n", host)
	return nil
}

func runGithubLogin(out io.Writer, hostOverride string) error {
	host := "github.com"
	if hostOverride != "" {
		host = hostOverride
	} else if cfg, err := config.Load(); err == nil {
		if h := githubauth.HostFromURL(cfg.GitHub.BaseURL); h != "" {
			host = h
		}
	}
	fmt.Fprintf(out, "GitHub auth for %s (same idea as GitLab):\n\n", host)
	fmt.Fprintf(out, "  1. Prefer:  gh auth login\n")
	fmt.Fprintf(out, "  2. Or set:  github.token in config.yaml\n")
	fmt.Fprintf(out, "  3. Or export GITHUB_TOKEN / GH_TOKEN\n\n")
	fmt.Fprintf(out, "Then verify with:  matterbox github status\n")
	return nil
}

func parseURLFromHost(webBase, hostOverride string) (string, error) {
	u, err := url.Parse(webBase)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("github: invalid base_url %q", webBase)
	}
	u.Host = hostOverride
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}
