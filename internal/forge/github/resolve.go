package github

import (
	"os"
	"strings"

	"matterbox/internal/githubauth"
)

// ResolveToken returns the GitHub access token the TUI/CLI should use, and a
// short label for which source won. Order matches GitLab's env-overrides-config
// pattern:
//
//  1. GITHUB_TOKEN
//  2. GH_TOKEN
//  3. configToken (github.token)
//  4. gh CLI hosts.yml for host
//  5. optional matterbox OAuth store (matterbox github login)
func ResolveToken(configToken, host string) (token, source string) {
	if t := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); t != "" {
		return t, "GITHUB_TOKEN"
	}
	if t := strings.TrimSpace(os.Getenv("GH_TOKEN")); t != "" {
		return t, "GH_TOKEN"
	}
	if t := strings.TrimSpace(configToken); t != "" {
		return t, "config github.token"
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return "", ""
	}
	if t := TokenFromGH(host); t != "" {
		return t, "gh CLI (~/.config/gh/hosts.yml)"
	}
	if t, _, err := githubauth.ReadTokenForHost(host); err == nil && strings.TrimSpace(t) != "" {
		return strings.TrimSpace(t), "matterbox OAuth (matterbox github login)"
	}
	return "", ""
}
