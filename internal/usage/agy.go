package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Antigravity (agy) quota API constants.
//
// The Antigravity CLI is a Google product and its allowances are the Gemini
// Code Assist ones: one bucket per model (and token type), each reporting the
// fraction still remaining and when it refills. Both calls are POSTs under
// the account's Google OAuth access token. The quota call must name the
// account's Code Assist project: without it Google answers 403
// SUBSCRIPTION_REQUIRED ("no valid license (#3501)") even for an account in
// good standing. loadCodeAssist, sent with the Antigravity client metadata,
// is what returns that project.
const (
	AgyQuotaURL          = "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota"
	AgyLoadCodeAssistURL = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
	// AgyUserAgent is what Code Assist expects an Antigravity client to
	// send. It is not decoration: under any other User-Agent loadCodeAssist
	// answers as if the account were never onboarded (allowed tiers only,
	// no project, no current tier), and the quota call then has no project
	// to name.
	AgyUserAgent  = "antigravity"
	agyTimeout    = 30 * time.Second
	agyWindowKind = "model_quota"

	// agyClientMetadata identifies the caller to loadCodeAssist the way the
	// Antigravity CLI does; Code Assist keys the project it returns on it.
	agyClientMetadata = `{"metadata":{"ideType":"ANTIGRAVITY","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}}`

	// agyRefreshHint tells the operator how the token gets renewed, since
	// caam will not do it.
	agyRefreshHint = "; agy renews its token when it runs — start agy once, then retry"
)

// AgyFetcher fetches usage data from the Antigravity quota API.
type AgyFetcher struct {
	client      *http.Client
	url         string // Overridable for testing
	loadURL     string // Overridable for testing
	userInfoURL string // Overridable for testing

	// The Code Assist project is a property of the account, so the last
	// answer is reused while the same token keeps being presented.
	mu              sync.Mutex
	projectToken    string
	projectForToken string
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

	info := &UsageInfo{
		Provider:  "agy",
		Source:    SourceAPI,
		FetchedAt: time.Now(),
	}

	project, err := f.codeAssistProject(ctx, accessToken)
	if err != nil {
		info.Error = err.Error()
		return info, err
	}

	status, body, err := f.post(ctx, f.resolveQuotaURL(), accessToken, `{"project":`+strconv.Quote(project)+`}`)
	if err != nil {
		info.Error = err.Error()
		return info, err
	}
	if err := agyStatusError(status, body); err != nil {
		info.Error = err.Error()
		return info, err
	}

	var quota agyQuotaResponse
	if err := json.Unmarshal(body, &quota); err != nil {
		info.Error = fmt.Sprintf("decode error: %v", err)
		return info, fmt.Errorf("decode response: %w", err)
	}

	applyAgyBuckets(info, quota.Buckets)
	return info, nil
}

// codeAssistProject returns the account's Gemini Code Assist project, the
// one the quota call must name. It comes from loadCodeAssist and is cached
// for as long as the same access token is presented.
func (f *AgyFetcher) codeAssistProject(ctx context.Context, accessToken string) (string, error) {
	f.mu.Lock()
	if f.projectToken == accessToken && f.projectForToken != "" {
		project := f.projectForToken
		f.mu.Unlock()
		return project, nil
	}
	f.mu.Unlock()

	status, body, err := f.post(ctx, f.resolveLoadURL(), accessToken, agyClientMetadata)
	if err != nil {
		return "", err
	}
	if err := agyStatusError(status, body); err != nil {
		return "", err
	}
	project := agyProjectOf(body)
	if project == "" {
		return "", fmt.Errorf("no Code Assist project on this account: Google has not onboarded it yet" + agyRefreshHint)
	}

	f.mu.Lock()
	f.projectToken, f.projectForToken = accessToken, project
	f.mu.Unlock()
	return project, nil
}

// agyProjectOf extracts cloudaicompanionProject from a loadCodeAssist
// response; Google sends it either as a bare id or as an object with one.
func agyProjectOf(body []byte) string {
	var payload struct {
		Project json.RawMessage `json:"cloudaicompanionProject"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Project) == 0 {
		return ""
	}
	var bare string
	if err := json.Unmarshal(payload.Project, &bare); err == nil {
		return strings.TrimSpace(bare)
	}
	var object struct {
		ID        string `json:"id"`
		ProjectID string `json:"projectId"`
	}
	if err := json.Unmarshal(payload.Project, &object); err == nil {
		if object.ID != "" {
			return strings.TrimSpace(object.ID)
		}
		return strings.TrimSpace(object.ProjectID)
	}
	return ""
}

// post sends one JSON request under the access token and returns the status
// and (bounded) body. A transport failure is returned as the error.
func (f *AgyFetcher) post(ctx context.Context, url, accessToken, body string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(body))
	if err != nil {
		return 0, nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", AgyUserAgent)

	resp, err := f.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read response: %w", err)
	}
	return resp.StatusCode, data, nil
}

// agyStatusError turns a non-200 answer into the error the caller reports.
// Google says why in the body ("invalid authentication credentials" is an
// expired token, "PERMISSION_DENIED" a scope or licence problem); keep that,
// it is what distinguishes a re-login from a caam bug. The Google access
// token lives an hour and agy renews it only when it runs; caam does not
// refresh it (that would mean holding agy's OAuth client and storing a
// credential agy did not write).
func agyStatusError(status int, body []byte) error {
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("unauthorized: token expired or invalid%s%s", googleErrorDetail(body), agyRefreshHint)
	default:
		return fmt.Errorf("API error: status %d%s", status, googleErrorDetail(body))
	}
}

// resolveLoadURL returns the loadCodeAssist endpoint: the test override,
// then the default. CAAM_AGY_QUOTA_URL changes only the quota call.
func (f *AgyFetcher) resolveLoadURL() string {
	if f.loadURL != "" {
		return f.loadURL
	}
	return AgyLoadCodeAssistURL
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
// body ({"error":{"code":..,"message":..,"status":..}}) as a short suffix:
// " (STATUS: message)" when both are present, else whichever one is.
func googleErrorDetail(body []byte) string {
	status, msg := errorField(body, "error.status"), errorField(body, "error.message")
	if status != "" && msg != "" {
		return " (" + status + ": " + msg + ")"
	}
	return errorDetail(body, "error.message", "error.status")
}

// agyOpaqueBucket reports a bucket Google names by an internal id rather
// than a model ("chat_20706"); those carry no reset and never move, so
// they are noise in a per-model table.
func agyOpaqueBucket(model string) bool {
	rest, ok := strings.CutPrefix(model, "chat_")
	if !ok || rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// applyAgyBuckets folds the quota buckets into info: every model keeps its
// own window under ModelWindows (the most-used bucket when a model reports
// several token types), the most-used pro bucket is the primary window and
// the most-used flash bucket the secondary one.
func applyAgyBuckets(info *UsageInfo, buckets []agyQuotaBucket) {
	for _, b := range buckets {
		model := strings.TrimSpace(b.ModelID)
		if model == "" || agyOpaqueBucket(model) {
			continue
		}
		remaining := 1.0
		if b.RemainingFraction != nil {
			remaining = clamp01(*b.RemainingFraction)
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
