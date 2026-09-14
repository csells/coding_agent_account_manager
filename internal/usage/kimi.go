package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/version"
)

// Kimi Code API constants.
//
// The Kimi Code CLI reads its own allowances from the managed endpoint it
// talks to for everything else: GET /coding/v1/usages with the account's
// OAuth access token. The response carries the weekly request allowance
// under "usage" and the shorter rate-limit windows (five hours today) under
// "limits", each as {limit, used, remaining, resetTime}, plus the membership
// level. The CLI sends a set of X-Msh-* device headers with every request
// and caam sends the same ones, built the same way, from the same
// per-install device id.
const (
	KimiCodeBaseURL = "https://api.kimi.com/coding/v1"
	KimiUsagePath   = "/usages"
	KimiMePath      = "/me"
	KimiUserAgent   = "caam/1.0"
	kimiTimeout     = 30 * time.Second
	kimiPlatform    = "kimi_code_cli"
	kimiHomeEnv     = "KIMI_CODE_HOME"
)

// KimiFetcher fetches usage data from the Kimi Code API.
type KimiFetcher struct {
	client  *http.Client
	baseURL string // Overridable for testing
	homeDir string // Overridable for testing; where device_id lives
}

// NewKimiFetcher creates a new Kimi Code usage fetcher.
func NewKimiFetcher() *KimiFetcher {
	return &KimiFetcher{client: &http.Client{Timeout: kimiTimeout}}
}

// kimiUsageResponse is the /usages response.
type kimiUsageResponse struct {
	Usage  *kimiUsageDetail `json:"usage"`
	Limits []kimiRateLimit  `json:"limits"`
	User   *struct {
		Membership *struct {
			Level string `json:"level"`
		} `json:"membership"`
	} `json:"user"`
	Version string `json:"version"`
}

type kimiRateLimit struct {
	Window struct {
		Duration int    `json:"duration"`
		TimeUnit string `json:"timeUnit"`
	} `json:"window"`
	Detail *kimiUsageDetail `json:"detail"`
}

// kimiUsageDetail is one allowance. The API writes its counters as strings.
type kimiUsageDetail struct {
	Limit     json.Number `json:"limit"`
	Used      json.Number `json:"used"`
	Remaining json.Number `json:"remaining"`
	ResetTime string      `json:"resetTime"`
}

func (d *kimiUsageDetail) window(duration time.Duration) *UsageWindow {
	if d == nil {
		return nil
	}
	limit, _ := d.Limit.Float64()
	used, _ := d.Used.Float64()
	if used == 0 {
		if remaining, err := d.Remaining.Float64(); err == nil && limit > 0 {
			used = limit - remaining
		}
	}
	util := 0.0
	if limit > 0 {
		util = clamp01(used / limit)
	}
	return &UsageWindow{
		Utilization:    util,
		UsedPercent:    int(util*100 + 0.5),
		ResetsAt:       parseISO8601(d.ResetTime),
		WindowDuration: duration,
	}
}

// kimiWindowDuration converts the API's {duration, timeUnit} to a Duration;
// 0 when the unit is unknown.
func kimiWindowDuration(duration int, unit string) time.Duration {
	d := time.Duration(duration)
	switch unit {
	case "TIME_UNIT_MINUTE":
		return d * time.Minute
	case "TIME_UNIT_HOUR":
		return d * time.Hour
	case "TIME_UNIT_DAY":
		return d * 24 * time.Hour
	case "TIME_UNIT_WEEK":
		return d * 7 * 24 * time.Hour
	}
	return 0
}

// kimiPlanName maps a membership level to the tier name Kimi sells it
// under (the V1 goods catalog); an unknown level is reported as-is.
func kimiPlanName(level, version string) string {
	level = strings.TrimSpace(level)
	if level == "" || level == "LEVEL_UNSPECIFIED" {
		return ""
	}
	if version != "" && version != "GOODS_VERSION_V1" {
		return level
	}
	switch level {
	case "LEVEL_FREE":
		return "adagio"
	case "LEVEL_TRIAL":
		return "andante"
	case "LEVEL_BASIC":
		return "moderato"
	case "LEVEL_INTERMEDIATE":
		return "allegretto"
	case "LEVEL_ADVANCED":
		return "allegro"
	}
	return level
}

func (f *KimiFetcher) base() string {
	return resolveBaseURL(f.baseURL, "KIMI_CODE_BASE_URL", KimiCodeBaseURL)
}

// resolveBaseURL is the override (tests) > env var > fallback ladder; the first two lose trailing slashes.
func resolveBaseURL(override, envVar, fallback string) string {
	if override != "" {
		return strings.TrimRight(override, "/")
	}
	if env := strings.TrimSpace(os.Getenv(envVar)); env != "" {
		return strings.TrimRight(env, "/")
	}
	return fallback
}

func (f *KimiFetcher) home() string {
	if f.homeDir != "" {
		return f.homeDir
	}
	return KimiHome()
}

// KimiHome is the Kimi Code CLI's home directory: $KIMI_CODE_HOME, else
// ~/.kimi-code. The device id and the credential file live under it.
func KimiHome() string {
	if env := strings.TrimSpace(os.Getenv(kimiHomeEnv)); env != "" {
		return env
	}
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".kimi-code")
}

// asciiHeader keeps a header value to printable ASCII, as the CLI does.
func asciiHeader(raw, fallback string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= 0x20 && r <= 0x7e {
			b.WriteRune(r)
		}
	}
	v := strings.TrimSpace(b.String())
	if v == "" {
		return fallback
	}
	return v
}

// setKimiHeaders adds the authorization and the device identity headers the
// Kimi Code CLI sends.
func (f *KimiFetcher) setKimiHeaders(req *http.Request, accessToken string) {
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	SetKimiDeviceHeaders(req, f.home())
}

// SetKimiDeviceHeaders adds the User-Agent and the X-Msh-* device identity
// headers the Kimi Code CLI sends with every request, to its API and to
// its OAuth host alike. The device id is read from <home>/device_id (home
// "" means KimiHome()) and never created: creating one is the CLI's job.
func SetKimiDeviceHeaders(req *http.Request, home string) {
	if home == "" {
		home = KimiHome()
	}
	req.Header.Set("User-Agent", KimiUserAgent)
	req.Header.Set("X-Msh-Platform", kimiPlatform)
	req.Header.Set("X-Msh-Version", asciiHeader(version.Version, "dev"))
	host, _ := os.Hostname()
	req.Header.Set("X-Msh-Device-Name", asciiHeader(host, "unknown"))
	req.Header.Set("X-Msh-Device-Model", asciiHeader(runtime.GOOS+" "+runtime.GOARCH, "unknown"))
	req.Header.Set("X-Msh-Os-Version", asciiHeader(runtime.GOOS, "unknown"))
	if data, err := os.ReadFile(filepath.Join(home, "device_id")); err == nil {
		if id := asciiHeader(string(data), ""); id != "" {
			req.Header.Set("X-Msh-Device-Id", id)
		}
	}
}

// kimiErrorDetail extracts an error message from a Kimi API error body,
// which names it as message, msg or error.
func kimiErrorDetail(body []byte) string {
	return errorDetail(body, "message", "msg", "error")
}

// Fetch retrieves usage data from the Kimi Code API.
func (f *KimiFetcher) Fetch(ctx context.Context, accessToken string) (*UsageInfo, error) {
	var usage kimiUsageResponse
	info, err := getJSON(ctx, f.client, jsonRequest{
		provider: "kimi",
		url:      f.base() + KimiUsagePath,
		token:    accessToken,
		headers:  func(req *http.Request) { SetKimiDeviceHeaders(req, f.home()) },
		unauthorized: func(detail string) string {
			return "unauthorized: token expired or invalid" + detail + "; refresh it (caam refresh kimi <account>, or r in the dashboard), then retry"
		},
		detail: kimiErrorDetail,
	}, &usage)
	if err != nil {
		return info, err
	}
	applyKimiUsage(info, &usage)
	return info, nil
}

// applyKimiUsage folds a /usages response onto info: the shortest
// rate-limit window is the primary window (a five-hour one today), the
// weekly request allowance is the secondary, and any further rate-limit
// windows are kept by duration under ModelWindows.
func applyKimiUsage(info *UsageInfo, usage *kimiUsageResponse) {
	if usage.User != nil && usage.User.Membership != nil {
		info.PlanType = kimiPlanName(usage.User.Membership.Level, usage.Version)
	}
	if w := usage.Usage.window(7 * 24 * time.Hour); w != nil {
		w.Kind = LimitKindWeeklyAll
		info.SecondaryWindow = w
	}
	for _, l := range usage.Limits {
		d := kimiWindowDuration(l.Window.Duration, l.Window.TimeUnit)
		w := l.Detail.window(d)
		if w == nil {
			continue
		}
		w.Kind = LimitKindSession
		if info.PrimaryWindow == nil || (d > 0 && d < info.PrimaryWindow.WindowDuration) {
			if info.PrimaryWindow != nil {
				keepKimiWindow(info, info.PrimaryWindow)
			}
			info.PrimaryWindow = w
			continue
		}
		keepKimiWindow(info, w)
	}
}

func keepKimiWindow(info *UsageInfo, w *UsageWindow) {
	if info.ModelWindows == nil {
		info.ModelWindows = make(map[string]*UsageWindow)
	}
	label := formatKimiWindowLabel(w.WindowDuration)
	w.Label = label
	info.ModelWindows[label] = w
}

func formatKimiWindowLabel(d time.Duration) string {
	if d <= 0 {
		return "window"
	}
	if d%(24*time.Hour) == 0 {
		return strconv.Itoa(int(d/(24*time.Hour))) + "d window"
	}
	if d%time.Hour == 0 {
		return strconv.Itoa(int(d/time.Hour)) + "h window"
	}
	return strconv.Itoa(int(d/time.Minute)) + "m window"
}

// KimiUserInfo returns the email (or, failing that, the username or
// nickname) of the account an access token belongs to, from /me.
func (f *KimiFetcher) KimiUserInfo(ctx context.Context, accessToken string) (string, error) {
	if accessToken == "" {
		return "", fmt.Errorf("access token is empty")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", f.base()+KimiMePath, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	f.setKimiHeaders(req, accessToken)
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
		return "", fmt.Errorf("me: status %d%s", resp.StatusCode, kimiErrorDetail(body))
	}
	var me struct {
		Email    string `json:"email"`
		Username string `json:"username"`
		Nickname string `json:"nickname"`
		UserID   string `json:"userId"`
	}
	if err := json.Unmarshal(body, &me); err != nil {
		return "", fmt.Errorf("decode me: %w", err)
	}
	for _, v := range []string{me.Email, me.Username, me.Nickname, me.UserID} {
		if v = strings.TrimSpace(v); v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("me carried no identity")
}

// ReadKimiCredentials reads the access token from a Kimi Code token file. A
// file with empty tokens is the CLI's logged-out state.
func ReadKimiCredentials(path string) (accessToken string, accountID string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	var creds struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		return "", "", err
	}
	if strings.TrimSpace(creds.AccessToken) == "" {
		return "", "", fmt.Errorf("no access token in kimi-code.json (logged out)")
	}
	return creds.AccessToken, "", nil
}
