package githubauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type deviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`

	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

type accessTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`

	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// DeviceStart holds the fields a CLI needs to present the user-code prompt
// and drive polling.
type DeviceStart struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresAt               time.Time
	Interval                time.Duration
}

// StartDeviceFlow requests a device code from GitHub's OAuth device flow
// endpoints. authBase must be the GitHub web origin, e.g.
// https://github.com or https://ghe.example.com.
func StartDeviceFlow(ctx context.Context, httpClient *http.Client, authBase, clientID string, scopes []string) (DeviceStart, error) {
	authBase = strings.TrimRight(strings.TrimSpace(authBase), "/")
	if authBase == "" {
		return DeviceStart{}, errors.New("githubauth: empty auth base")
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return DeviceStart{}, errors.New("githubauth: empty client_id")
	}

	form := url.Values{}
	form.Set("client_id", clientID)
	if len(scopes) > 0 {
		form.Set("scope", strings.Join(scopes, " "))
	}

	endpoint := authBase + "/login/device/code"
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return DeviceStart{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return DeviceStart{}, err
	}
	defer resp.Body.Close()

	var body deviceCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return DeviceStart{}, fmt.Errorf("githubauth: parse device code response: %w", err)
	}
	if body.Error != "" {
		desc := strings.TrimSpace(body.ErrorDescription)
		if desc == "" {
			desc = body.Error
		}
		return DeviceStart{}, fmt.Errorf("githubauth: device code error: %s", desc)
	}
	if body.DeviceCode == "" || body.UserCode == "" {
		return DeviceStart{}, errors.New("githubauth: device code response missing fields")
	}

	interval := 5 * time.Second
	if body.Interval > 0 {
		interval = time.Duration(body.Interval) * time.Second
	}
	expiresAt := time.Now().Add(time.Duration(body.ExpiresIn) * time.Second)
	if body.ExpiresIn <= 0 {
		// GitHub's default is 900s; keep things safe.
		expiresAt = time.Now().Add(15 * time.Minute)
	}

	return DeviceStart{
		DeviceCode:              body.DeviceCode,
		UserCode:                body.UserCode,
		VerificationURI:         body.VerificationURI,
		VerificationURIComplete: body.VerificationURIComplete,
		ExpiresAt:               expiresAt,
		Interval:                interval,
	}, nil
}

// PollDeviceFlow polls until the device code is authorized and GitHub
// returns an access_token, or until expiresAt is reached / ctx is cancelled.
func PollDeviceFlow(ctx context.Context, httpClient *http.Client, authBase, clientID string, start DeviceStart) (string, error) {
	if start.DeviceCode == "" {
		return "", errors.New("githubauth: empty device_code")
	}

	interval := start.Interval
	next := time.NewTimer(0)
	defer next.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-next.C:
		}

		if time.Now().After(start.ExpiresAt) {
			return "", errors.New("githubauth: device code expired")
		}

		form := url.Values{}
		form.Set("client_id", clientID)
		form.Set("device_code", start.DeviceCode)
		form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

		endpoint := authBase + "/login/oauth/access_token"
		req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")

		resp, err := httpClient.Do(req)
		if err != nil {
			// Temporary network hiccup: retry after the interval.
			next.Reset(interval)
			continue
		}
		var body accessTokenResponse
		decodeErr := json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if decodeErr != nil {
			next.Reset(interval)
			continue
		}

		if body.AccessToken != "" {
			return body.AccessToken, nil
		}

		switch body.Error {
		case "authorization_pending", "":
			next.Reset(interval)
		case "slow_down":
			interval += 5 * time.Second
			next.Reset(interval)
		case "expired_token":
			return "", errors.New("githubauth: device code expired")
		case "access_denied":
			return "", errors.New("githubauth: access denied")
		default:
			desc := strings.TrimSpace(body.ErrorDescription)
			if desc == "" {
				desc = body.Error
			}
			return "", fmt.Errorf("githubauth: device polling error: %s", desc)
		}
	}
}

// APIBaseFromWebBase converts a GitHub web origin into the corresponding API
// base for REST calls.
//
// For github.com: https://api.github.com
// For enterprise: https://<host>/api/v3
func APIBaseFromWebBase(webBase string) (string, error) {
	webBase = strings.TrimRight(strings.TrimSpace(webBase), "/")
	if webBase == "" {
		return "", errors.New("githubauth: empty base_url")
	}
	if !strings.Contains(webBase, "://") {
		webBase = "https://" + webBase
	}
	u, err := url.Parse(webBase)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("githubauth: parse base_url: %w", err)
	}
	if strings.EqualFold(u.Host, "github.com") {
		return "https://api.github.com", nil
	}
	// Host-rooted /api/v3 — ignore any path on the web origin so this matches
	// the TUI client's apiBaseFromWebBase.
	return u.Scheme + "://" + u.Host + "/api/v3", nil
}

// HostFromURL extracts the lowercased hostname from a GitHub web origin.
func HostFromURL(webBase string) string {
	webBase = strings.TrimSpace(webBase)
	if webBase == "" {
		return ""
	}
	if !strings.Contains(webBase, "://") {
		webBase = "https://" + webBase
	}
	u, err := url.Parse(webBase)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Host)
}

// VerifyAccessToken calls GET <apiBase>/user and returns the login name.
func VerifyAccessToken(ctx context.Context, httpClient *http.Client, apiBase, token string) (string, error) {
	apiBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if apiBase == "" {
		return "", errors.New("githubauth: empty api base")
	}
	if token == "" {
		return "", errors.New("githubauth: empty token")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", apiBase+"/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("githubauth: token verify failed with HTTP %d", resp.StatusCode)
	}

	var body struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("githubauth: parse /user response: %w", err)
	}
	if body.Login == "" {
		return "", errors.New("githubauth: /user response missing login")
	}
	return body.Login, nil
}
