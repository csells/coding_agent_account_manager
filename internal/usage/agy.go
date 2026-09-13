package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// Antigravity (agy) quota API constants.
//
// The Antigravity CLI is a Google product and its allowances are the Gemini
// Code Assist ones: one bucket per model (and token type), each reporting the
// fraction still remaining and when it refills. The call is a POST with an
// empty body under the account's Google OAuth access token.
const (
	AgyQuotaURL   = "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota"
	AgyUserAgent  = "caam/1.0"
	agyTimeout    = 30 * time.Second
	agyWindowKind = "model_quota"

	// agyRefreshHint tells the operator how the token gets renewed, since
	// caam will not do it.
	agyRefreshHint = "; agy renews its token when it runs — start agy once, then retry"
)

// AgyFetcher fetches usage data from the Antigravity quota API.
type AgyFetcher struct {
	client      *http.Client
	url         string // Overridable for testing
	userInfoURL string // Overridable for testing
}

// NewAgyFetcher creates a new Antigravity usage fetcher.
func NewAgyFetcher() *AgyFetcher {
	return &AgyFetcher{client: &http.Client{Timeout: agyTimeout}}
}

// agyQuotaResponse is the retrieveUserQuota response: a list of buckets.
type agyQuotaResponse struct {
	Buckets []agyQuotaBucket `json:"buckets"`
}

// agyQuotaBucket is one model's allowance. remainingFraction defaults to 1.0
// (untouched) when absent; a bucket without a modelId is skipped.
type agyQuotaBucket struct {
	RemainingFraction *float64 `json:"remainingFraction"`
	ResetTime         string   `json:"resetTime"`
	ModelID           string   `json:"modelId"`
	TokenType         string   `json:"tokenType"`
}

// agyTier ranks a model id for the primary/secondary slots: pro models are
// the primary window, flash the secondary, flash-lite after flash, anything
// else after that.
func agyTier(modelID string) int {
	id := strings.ToLower(modelID)
	switch {
	case strings.Contains(id, "flash-lite"):
		return 2
	case strings.Contains(id, "flash"):
		return 1
	case strings.Contains(id, "pro"):
		return 0
	}
	return 3
}

// Fetch retrieves usage data from the Antigravity quota API.
func (f *AgyFetcher) Fetch(ctx context.Context, accessToken string) (*UsageInfo, error) {
	if accessToken == "" {
		return nil, fmt.Errorf("access token is empty")
	}

	url := f.resolveQuotaURL()

	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader("{}"))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", AgyUserAgent)

	resp, err := f.client.Do(req)
	if err != nil {
		return &UsageInfo{
			Provider:  "agy",
			FetchedAt: time.Now(),
			Error:     fmt.Sprintf("request failed: %v", err),
		}, err
	}
	defer resp.Body.Close()

	info := &UsageInfo{
		Provider:  "agy",
		Source:    SourceAPI,
		FetchedAt: time.Now(),
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		info.Error = fmt.Sprintf("read response: %v", err)
		return info, fmt.Errorf("read response: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		// Google says why in the body ("invalid authentication credentials"
		// is an expired token, "PERMISSION_DENIED" a scope or endpoint
		// problem); keep that, it is what distinguishes a re-login from a
		// caam bug.
		// The Google access token lives an hour and agy renews it only when
		// it runs; caam does not refresh it (that would mean holding agy's
		// OAuth client and storing a credential agy did not write).
		info.Error = "unauthorized: token expired or invalid" + googleErrorDetail(body) + agyRefreshHint
		return info, fmt.Errorf("unauthorized: status %d%s%s", resp.StatusCode, googleErrorDetail(body), agyRefreshHint)
	default:
		info.Error = fmt.Sprintf("API error: status %d%s", resp.StatusCode, googleErrorDetail(body))
		return info, fmt.Errorf("API error: status %d%s", resp.StatusCode, googleErrorDetail(body))
	}

	var quota agyQuotaResponse
	if err := json.Unmarshal(body, &quota); err != nil {
		info.Error = fmt.Sprintf("decode error: %v", err)
		return info, fmt.Errorf("decode response: %w", err)
	}

	applyAgyBuckets(info, quota.Buckets)
	return info, nil
}

// resolveQuotaURL returns the quota endpoint: the test override, then
// CAAM_AGY_QUOTA_URL (a different v1internal method, or a proxy in front of
// it), then the default.
func (f *AgyFetcher) resolveQuotaURL() string {
	if f.url != "" {
		return f.url
	}
	if env := strings.TrimSpace(os.Getenv("CAAM_AGY_QUOTA_URL")); env != "" {
		return env
	}
	return AgyQuotaURL
}

// googleErrorDetail extracts the status and message of a Google API error
// body ({"error":{"code":..,"message":..,"status":..}}) as a short suffix.
// Error bodies carry no credential.
func googleErrorDetail(body []byte) string {
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	msg := strings.TrimSpace(payload.Error.Message)
	if len(msg) > 160 {
		msg = msg[:157] + "..."
	}
	switch {
	case payload.Error.Status != "" && msg != "":
		return fmt.Sprintf(" (%s: %s)", payload.Error.Status, msg)
	case msg != "":
		return " (" + msg + ")"
	case payload.Error.Status != "":
		return " (" + payload.Error.Status + ")"
	}
	return ""
}

// applyAgyBuckets folds the quota buckets into info: every model keeps its
// own window under ModelWindows (the most-used bucket when a model reports
// several token types), the most-used pro bucket is the primary window and
// the most-used flash bucket the secondary one.
func applyAgyBuckets(info *UsageInfo, buckets []agyQuotaBucket) {
	for _, b := range buckets {
		model := strings.TrimSpace(b.ModelID)
		if model == "" {
			continue
		}
		remaining := 1.0
		if b.RemainingFraction != nil {
			remaining = *b.RemainingFraction
		}
		if remaining < 0 {
			remaining = 0
		}
		if remaining > 1 {
			remaining = 1
		}
		used := 1 - remaining
		w := &UsageWindow{
			Utilization: used,
			UsedPercent: int(used*100 + 0.5),
			ResetsAt:    parseISO8601(b.ResetTime),
			Label:       model,
			Kind:        agyWindowKind,
		}
		if info.ModelWindows == nil {
			info.ModelWindows = make(map[string]*UsageWindow)
		}
		if prev, ok := info.ModelWindows[model]; !ok || w.Utilization > prev.Utilization {
			info.ModelWindows[model] = w
		}
	}

	// Deterministic slot assignment: within a tier the most-used model wins,
	// ties broken by name.
	models := make([]string, 0, len(info.ModelWindows))
	for m := range info.ModelWindows {
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool {
		ti, tj := agyTier(models[i]), agyTier(models[j])
		if ti != tj {
			return ti < tj
		}
		ui, uj := info.ModelWindows[models[i]].Utilization, info.ModelWindows[models[j]].Utilization
		if ui != uj {
			return ui > uj
		}
		return models[i] < models[j]
	})
	for _, m := range models {
		switch agyTier(m) {
		case 0:
			if info.PrimaryWindow == nil {
				info.PrimaryWindow = info.ModelWindows[m]
			}
		case 1:
			if info.SecondaryWindow == nil {
				info.SecondaryWindow = info.ModelWindows[m]
			}
		}
	}
}

// AgyUserInfoURL is Google's OpenID userinfo endpoint. The Antigravity token
// carries the userinfo.email scope, and no agy file records which Google
// account is signed in (google_accounts.json belongs to the legacy Gemini
// CLI), so this is the only source of the account's identity.
const AgyUserInfoURL = "https://www.googleapis.com/oauth2/v3/userinfo"

// AgyUserInfo returns the email of the Google account an Antigravity access
// token belongs to. The token travels in the Authorization header, never in
// the URL.
func (f *AgyFetcher) AgyUserInfo(ctx context.Context, accessToken string) (string, error) {
	if accessToken == "" {
		return "", fmt.Errorf("access token is empty")
	}
	url := AgyUserInfoURL
	if f.userInfoURL != "" {
		url = f.userInfoURL
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", AgyUserAgent)

	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("userinfo: status %d%s", resp.StatusCode, googleErrorDetail(body))
	}
	var info struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return "", fmt.Errorf("decode userinfo: %w", err)
	}
	if strings.TrimSpace(info.Email) == "" {
		return "", fmt.Errorf("userinfo carried no email")
	}
	return strings.TrimSpace(info.Email), nil
}

// ReadAgyCredentials reads the Google OAuth access token from an Antigravity
// token file: the JSON agy stores as antigravity-oauth-token on Linux and in
// the login keychain (mirrored to that path) on macOS. The token is nested
// under "token" as an oauth2 token object; a flat layout and a bare token
// string are accepted too.
func ReadAgyCredentials(path string) (accessToken string, accountID string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	var root struct {
		Token       json.RawMessage `json:"token"`
		AccessToken string          `json:"access_token"`
		AccessCamel string          `json:"accessToken"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return "", "", fmt.Errorf("parse antigravity token: %w", err)
	}
	if len(root.Token) > 0 {
		var nested struct {
			AccessToken string `json:"access_token"`
			AccessCamel string `json:"accessToken"`
		}
		if err := json.Unmarshal(root.Token, &nested); err == nil {
			if nested.AccessToken != "" {
				return nested.AccessToken, "", nil
			}
			if nested.AccessCamel != "" {
				return nested.AccessCamel, "", nil
			}
		}
		var bare string
		if err := json.Unmarshal(root.Token, &bare); err == nil && bare != "" {
			return bare, "", nil
		}
	}
	if root.AccessToken != "" {
		return root.AccessToken, "", nil
	}
	if root.AccessCamel != "" {
		return root.AccessCamel, "", nil
	}
	return "", "", fmt.Errorf("no access token found in antigravity token file")
}
