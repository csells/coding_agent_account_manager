package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/zcodecred"
)

// zcode API constants.
//
// zcode is Z.ai's coding harness. Its own billing endpoint,
// GET /api/v1/zcode-plan/billing/current on zcode.z.ai, answers to the
// zcode session JWT (not the Z.ai OAuth access token) with
// {"code":0,"msg":"","data":{"server_time":..,"plans":[...]}} where plans
// lists the coding plans the account holds. An account with no coding plan
// answers with an empty list, which is reported as exactly that rather than
// as 0% used.
const (
	ZcodeOrigin      = "https://zcode.z.ai"
	ZcodeBillingPath = "/api/v1/zcode-plan/billing/current"
	ZcodeUserAgent   = "caam/1.0"
	zcodeTimeout     = 30 * time.Second
)

// ErrNoZcodePlan is the row error for a zcode login without a coding plan.
const ErrNoZcodePlan = "no Z.ai coding plan on this account"

// ZcodeFetcher fetches usage data from zcode's billing API.
type ZcodeFetcher struct {
	client  *http.Client
	baseURL string // Overridable for testing
}

// NewZcodeFetcher creates a new zcode usage fetcher.
func NewZcodeFetcher() *ZcodeFetcher {
	return &ZcodeFetcher{client: &http.Client{Timeout: zcodeTimeout}}
}

func (f *ZcodeFetcher) base() string {
	return resolveBaseURL(f.baseURL, "ZCODE_BASE_URL", ZcodeOrigin)
}

// zcodeBillingResponse is the envelope every zcode API answer uses.
type zcodeBillingResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		ServerTime int64             `json:"server_time"`
		Plans      []json.RawMessage `json:"plans"`
	} `json:"data"`
}

// zcodePlan is the subset of a plan entry caam reads. The exact field
// names of a held plan have not been observed (no account with a plan was
// available when this was written), so the common spellings are all
// accepted and a plan that yields no usable figure is reported by name.
type zcodePlan struct {
	Name       string  `json:"name"`
	PlanName   string  `json:"plan_name"`
	Type       string  `json:"type"`
	Percentage float64 `json:"percentage"`
	Percent    float64 `json:"percent"`
	UsedPct    float64 `json:"used_percent"`
	Usage      float64 `json:"usage"`
	Used       float64 `json:"used"`
	Limit      float64 `json:"limit"`
	Total      float64 `json:"total"`
	Remaining  float64 `json:"remaining"`
	NextReset  any     `json:"next_reset_time"`
	ResetTime  any     `json:"reset_time"`
	ExpireTime any     `json:"expire_time"`
}

func (p *zcodePlan) label() string {
	for _, v := range []string{p.PlanName, p.Name, p.Type} {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return "coding plan"
}

// usedFraction returns the used share and whether any figure was present.
func (p *zcodePlan) usedFraction() (float64, bool) {
	switch {
	case p.Percentage > 0:
		return clamp01(p.Percentage / 100), true
	case p.Percent > 0:
		return clamp01(p.Percent / 100), true
	case p.UsedPct > 0:
		return clamp01(p.UsedPct / 100), true
	}
	total := p.Limit
	if total == 0 {
		total = p.Total
	}
	if total == 0 {
		total = p.Usage
	}
	if total <= 0 {
		return 0, false
	}
	used := p.Used
	if used == 0 && p.Remaining > 0 {
		used = total - p.Remaining
	}
	return clamp01(used / total), true
}

// zcodeTime reads a reset time given as epoch seconds, epoch milliseconds,
// or an RFC 3339 string.
func zcodeTime(v any) time.Time {
	switch t := v.(type) {
	case float64:
		if t <= 0 {
			return time.Time{}
		}
		if t > 1e12 {
			return time.UnixMilli(int64(t))
		}
		return time.Unix(int64(t), 0)
	case string:
		return parseISO8601(t)
	}
	return time.Time{}
}

// Fetch retrieves usage data from zcode's billing API. accessToken is the
// zcode session JWT.
func (f *ZcodeFetcher) Fetch(ctx context.Context, accessToken string) (*UsageInfo, error) {
	var billing zcodeBillingResponse
	info, err := getJSON(ctx, f.client, jsonRequest{
		provider: "zcode",
		url:      f.base() + ZcodeBillingPath,
		token:    accessToken,
		headers:  func(req *http.Request) { req.Header.Set("User-Agent", ZcodeUserAgent) },
		unauthorized: func(string) string {
			return "unauthorized: token expired or invalid; run `zcode login`, then retry"
		},
	}, &billing)
	if err != nil {
		return info, err
	}
	if billing.Code != 0 {
		info.Error = fmt.Sprintf("API error: code %d %s", billing.Code, strings.TrimSpace(billing.Msg))
		return info, fmt.Errorf("API error: code %d %s", billing.Code, strings.TrimSpace(billing.Msg))
	}
	if len(billing.Data.Plans) == 0 {
		// A login without a plan is not an idle plan.
		info.Error = ErrNoZcodePlan
		return info, nil
	}
	applyZcodePlans(info, billing.Data.Plans)
	return info, nil
}

// applyZcodePlans folds the plan entries onto info: the first plan with a
// usable figure is the primary window, the second the secondary, and every
// plan keeps a window under its own name. A plan whose fields caam does not
// recognise is named in the row error so its shape can be added.
func applyZcodePlans(info *UsageInfo, plans []json.RawMessage) {
	var unknown []string
	for _, raw := range plans {
		var p zcodePlan
		if err := json.Unmarshal(raw, &p); err != nil {
			unknown = append(unknown, "unparseable plan")
			continue
		}
		used, ok := p.usedFraction()
		if !ok {
			unknown = append(unknown, p.label())
			continue
		}
		w := &UsageWindow{
			Utilization: used,
			UsedPercent: int(used*100 + 0.5),
			Label:       p.label(),
		}
		for _, v := range []any{p.NextReset, p.ResetTime, p.ExpireTime} {
			if t := zcodeTime(v); !t.IsZero() {
				w.ResetsAt = t
				break
			}
		}
		if info.ModelWindows == nil {
			info.ModelWindows = make(map[string]*UsageWindow)
		}
		info.ModelWindows[w.Label] = w
		switch {
		case info.PrimaryWindow == nil:
			info.PrimaryWindow = w
		case info.SecondaryWindow == nil:
			info.SecondaryWindow = w
		}
	}
	if len(unknown) > 0 && info.PrimaryWindow == nil {
		info.Error = "coding plan present but its usage fields were not recognised: " + strings.Join(unknown, ", ")
	}
}

// ReadZcodeCredentials unseals a zcode credential record and returns the
// session JWT its billing API accepts, with the Z.ai user id as the
// account id.
func ReadZcodeCredentials(path string) (accessToken string, accountID string, err error) {
	rec, err := zcodecred.ReadRecord(path)
	if err != nil {
		return "", "", err
	}
	if rec.UserInfo != nil {
		accountID = rec.UserInfo.UserID
	}
	if rec.JWTToken == "" {
		return "", accountID, fmt.Errorf("no zcode session token in credentials.json (logged out)")
	}
	return rec.JWTToken, accountID, nil
}
