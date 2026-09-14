package refresh

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// Kimi Code refreshes its access token the way its CLI does: a form-encoded
// POST to {oauthHost}/api/oauth/token with the CLI's client id and the
// refresh token, sent with the same X-Msh-* device headers as every other
// request the CLI makes (internal/usage builds them). The answer carries
// access_token, expires_in (seconds) and, when the family rotates, a new
// refresh_token. 401, 403 or invalid_grant mean the session is gone.
const (
	// KimiClientID is the OAuth client the Kimi Code CLI identifies as.
	KimiClientID = "17e5f671-d194-4dfb-9706-5516cb48c098"
	// KimiTokenPath is the token endpoint under the OAuth host.
	KimiTokenPath = "/api/oauth/token"
	// kimiDefaultOAuthHost is the CLI's default OAuth host.
	kimiDefaultOAuthHost = "https://auth.kimi.com"
	// kimiCredentialFile is the token file in the vault and under the
	// CLI's credentials directory alike.
	kimiCredentialFile = "kimi-code.json"
)

// kimiTokenHosts are the only hosts a Kimi refresh token is presented to
// (plus loopback, for tests).
var kimiTokenHosts = []string{"auth.kimi.com", "auth.kimi.ai"}

// KimiOAuthHost is the OAuth host the Kimi Code CLI would refresh against:
// KIMI_CODE_OAUTH_HOST, then KIMI_OAUTH_HOST, then https://auth.kimi.com.
// The token endpoint is KimiTokenPath under it; tests point the CLI's
// override at a loopback server.
func KimiOAuthHost() string {
	for _, key := range []string{"KIMI_CODE_OAUTH_HOST", "KIMI_OAUTH_HOST"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	return kimiDefaultOAuthHost
}

// RefreshKimiToken presents a Kimi Code refresh token and returns the new
// tokens. A 401, 403 or invalid_grant answer is reported as
// ErrRefreshTokenReused: the session is gone and only a new login fixes it.
var RefreshKimiToken = func(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is empty")
	}

	endpoint := KimiOAuthHost() + KimiTokenPath
	if err := validateTokenEndpoint(endpoint, kimiTokenHosts); err != nil {
		return nil, err
	}

	form := url.Values{}
	form.Set("client_id", KimiClientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	usage.SetKimiDeviceHeaders(req, "")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kimi refresh failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := readLimitedBody(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("kimi refresh error %d (failed to read body: %v)", resp.StatusCode, err)
	}

	if resp.StatusCode != http.StatusOK {
		if kimiSessionGone(resp.StatusCode, body) {
			return nil, fmt.Errorf("%w: kimi refresh refused with status %d", ErrRefreshTokenReused, resp.StatusCode)
		}
		return nil, fmt.Errorf("kimi refresh error %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tokenResp TokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("kimi refresh answered without an access token")
	}
	return &tokenResp, nil
}

// kimiSessionGone reports whether the token endpoint's answer says the
// refresh token no longer works: unauthorized, forbidden, or the OAuth
// invalid_grant error.
func kimiSessionGone(status int, body []byte) bool {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return true
	}
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(payload.Error), "invalid_grant")
}

// UpdateKimiAuth writes the new tokens into a kimi-code.json: access_token,
// refresh_token (when the answer carries one), expires_at (unix seconds)
// and expires_in. Everything else in the file stays as the CLI wrote it.
func UpdateKimiAuth(path string, resp *TokenResponse) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read auth file: %w", err)
	}
	var auth map[string]interface{}
	if err := json.Unmarshal(data, &auth); err != nil {
		return fmt.Errorf("parse auth file: %w", err)
	}
	if auth == nil {
		auth = map[string]interface{}{}
	}

	auth["access_token"] = resp.AccessToken
	if resp.RefreshToken != "" {
		auth["refresh_token"] = resp.RefreshToken
	}
	if resp.ExpiresIn > 0 {
		auth["expires_at"] = time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second).Unix()
		auth["expires_in"] = resp.ExpiresIn
	}
	return writeAuthFile(path, auth)
}
