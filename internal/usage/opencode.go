package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// OpenCode usage constants.
//
// OpenCode's Console login (the account rows in opencode.db) exposes no
// usage API that a bearer token reaches: the dashboard reads it through
// browser-session server functions. What OpenCode does publish is the Zen /
// OpenCode Go usage endpoint, GET https://opencode.ai/zen/go/v1/usage,
// authenticated by a Zen API key (OPENCODE_API_KEY), which reports the
// rolling, weekly and monthly windows as percentages. caam presents that
// key when the OpenCode store holds one; otherwise the row says plainly
// that this login has no limits API.
const (
	OpenCodeUsageURL  = "https://opencode.ai/zen/go/v1/usage"
	OpenCodeUserAgent = "caam/1.0"
	opencodeTimeout   = 30 * time.Second
)

// ErrNoOpenCodeLimitsAPI is the row error for an OpenCode login that holds
// no Zen API key.
const ErrNoOpenCodeLimitsAPI = "no limits API for this login: OpenCode exposes usage only through its web dashboard; a Zen API key (OPENCODE_API_KEY) in OpenCode's credentials enables it"

// OpenCodeFetcher fetches usage data from the OpenCode Zen usage API.
type OpenCodeFetcher struct {
	client *http.Client
	url    string // Overridable for testing
}

// NewOpenCodeFetcher creates a new OpenCode usage fetcher.
func NewOpenCodeFetcher() *OpenCodeFetcher {
	return &OpenCodeFetcher{client: &http.Client{Timeout: opencodeTimeout}}
}

// opencodeUsageResponse is the Zen usage response: percentages in 0-100.
type opencodeUsageResponse struct {
	Usage struct {
		Rolling *opencodeWindow `json:"rolling"`
		Weekly  *opencodeWindow `json:"weekly"`
		Monthly *opencodeWindow `json:"monthly"`
	} `json:"usage"`
}

type opencodeWindow struct {
	Percent    float64 `json:"percent"`
	ResetInSec float64 `json:"resetInSec"`
	ResetAt    string  `json:"resetAt"`
}

func (w *opencodeWindow) window(now time.Time, duration time.Duration) *UsageWindow {
	if w == nil {
		return nil
	}
	pct := w.Percent
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	out := &UsageWindow{
		Utilization:    pct / 100,
		UsedPercent:    int(pct + 0.5),
		WindowDuration: duration,
	}
	switch {
	case w.ResetInSec > 0:
		out.ResetsAt = now.Add(time.Duration(w.ResetInSec * float64(time.Second)))
	case w.ResetAt != "":
		out.ResetsAt = parseISO8601(w.ResetAt)
	}
	return out
}

// Fetch retrieves usage data from the Zen usage API with a Zen API key.
func (f *OpenCodeFetcher) Fetch(ctx context.Context, apiKey string) (*UsageInfo, error) {
	url := OpenCodeUsageURL
	if f.url != "" {
		url = f.url
	}
	var usage opencodeUsageResponse
	info, err := getJSON(ctx, f.client, jsonRequest{
		provider:     "opencode",
		url:          url,
		token:        apiKey,
		headers:      func(req *http.Request) { req.Header.Set("User-Agent", OpenCodeUserAgent) },
		unauthorized: func(string) string { return "unauthorized: Zen API key rejected" },
	}, &usage)
	if err != nil {
		return info, err
	}
	// The windows' reset clocks are relative to the fetch.
	now := info.FetchedAt
	if usage.Usage.Rolling == nil && usage.Usage.Weekly == nil && usage.Usage.Monthly == nil {
		info.Error = "decode error: response carries no usage windows"
		return info, fmt.Errorf("decode response: no usage windows")
	}
	info.PrimaryWindow = usage.Usage.Rolling.window(now, 5*time.Hour)
	info.SecondaryWindow = usage.Usage.Weekly.window(now, 7*24*time.Hour)
	if monthly := usage.Usage.Monthly.window(now, 30*24*time.Hour); monthly != nil {
		monthly.Label = "monthly"
		info.ModelWindows = map[string]*UsageWindow{"monthly": monthly}
	}
	return info, nil
}

// ReadOpenCodeCredentials reads the Zen API key out of a vault export of
// OpenCode's store (opencode-auth.json) or an older auth.json. The account
// id is the active Console account's email. A store without a Zen key
// returns ErrNoOpenCodeLimitsAPI as the error text, so the caller can show
// it as the row's status rather than as a missing profile.
func ReadOpenCodeCredentials(path string) (accessToken string, accountID string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return "", "", err
	}
	if tables, ok := root["tables"]; ok {
		return openCodeExportCredentials(tables)
	}
	// Legacy auth.json: {"opencode": {"type":"api","key":"..."}, ...}.
	if raw, ok := root["opencode"]; ok {
		if key := openCodeKeyFromValue(string(raw)); key != "" {
			return key, "", nil
		}
	}
	return "", "", fmt.Errorf("%s", ErrNoOpenCodeLimitsAPI)
}

func openCodeExportCredentials(tables json.RawMessage) (string, string, error) {
	var t struct {
		Account []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"account"`
		AccountState []struct {
			ActiveAccountID string `json:"active_account_id"`
		} `json:"account_state"`
		Credential []struct {
			IntegrationID string `json:"integration_id"`
			MethodID      string `json:"method_id"`
			Value         string `json:"value"`
		} `json:"credential"`
	}
	if err := json.Unmarshal(tables, &t); err != nil {
		return "", "", err
	}
	accountID := ""
	for _, st := range t.AccountState {
		for _, a := range t.Account {
			if a.ID == st.ActiveAccountID {
				accountID = a.Email
			}
		}
	}
	if accountID == "" && len(t.Account) > 0 {
		accountID = t.Account[0].Email
	}
	for _, c := range t.Credential {
		id := strings.ToLower(c.IntegrationID)
		if id != "opencode" && id != "opencode-go" && id != "opencode-zen" {
			continue
		}
		if key := openCodeKeyFromValue(c.Value); key != "" {
			return key, accountID, nil
		}
	}
	return "", accountID, fmt.Errorf("%s", ErrNoOpenCodeLimitsAPI)
}

// openCodeKeyFromValue extracts an API key from a credential value: a bare
// key, or the {"type":"api","key":...} object auth.json used. An OAuth
// value (a Console session) is not a Zen key.
func openCodeKeyFromValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.HasPrefix(value, "{") {
		return value
	}
	var v struct {
		Type string `json:"type"`
		Key  string `json:"key"`
	}
	if err := json.Unmarshal([]byte(value), &v); err != nil {
		return ""
	}
	if v.Type != "" && v.Type != "api" {
		return ""
	}
	return strings.TrimSpace(v.Key)
}
