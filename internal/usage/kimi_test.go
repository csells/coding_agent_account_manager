package usage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Kimi Code /usages response, as the CLI receives it: the weekly request
// allowance under "usage", the five-hour rate limit under "limits", counters
// as strings, and the membership level.
const kimiUsagePayload = `{
  "usage": {"limit":"2048","used":"214","remaining":"1834","resetTime":"2030-01-09T15:23:13.716839300Z"},
  "limits": [{"window":{"duration":300,"timeUnit":"TIME_UNIT_MINUTE"},"detail":{"limit":"200","used":"139","remaining":"61","resetTime":"2030-01-06T13:33:02.717479433Z"}}],
  "user": {"membership":{"level":"LEVEL_BASIC"}},
  "version": "GOODS_VERSION_V1"
}`

func TestKimiFetcher_Fetch_MapsAllowancesOntoWindows(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "device_id"), []byte("dev-1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got http.Header
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, kimiUsagePayload)
	}))
	defer server.Close()

	f := NewKimiFetcher()
	f.baseURL = server.URL
	f.homeDir = home
	info, err := f.Fetch(context.Background(), "SYNTHETIC-KIMI")
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if gotPath != KimiUsagePath || got.Get("Authorization") != "Bearer SYNTHETIC-KIMI" {
		t.Fatalf("request = %s auth=%q", gotPath, got.Get("Authorization"))
	}
	// The CLI's device identity headers travel with the request; the device
	// id comes from the CLI's own file and is not invented.
	if got.Get("X-Msh-Platform") != "kimi_code_cli" || got.Get("X-Msh-Device-Id") != "dev-1234" || got.Get("X-Msh-Version") == "" {
		t.Errorf("identity headers = %v", got)
	}
	if info.PlanType != "moderato" {
		t.Errorf("PlanType = %q, want moderato", info.PlanType)
	}
	// Five-hour rate limit is primary (139/200), weekly allowance secondary.
	if info.PrimaryWindow == nil || info.PrimaryWindow.UsedPercent != 70 || info.PrimaryWindow.WindowDuration.Hours() != 5 {
		t.Fatalf("primary = %+v", info.PrimaryWindow)
	}
	if info.SecondaryWindow == nil || info.SecondaryWindow.UsedPercent != 10 || info.SecondaryWindow.ResetsAt.IsZero() {
		t.Fatalf("secondary = %+v", info.SecondaryWindow)
	}
	if info.PrimaryWindow.ResetsAt.Day() != 6 {
		t.Errorf("primary reset = %v", info.PrimaryWindow.ResetsAt)
	}

	// No device_id on disk: the header is simply absent, nothing is created.
	f.homeDir = t.TempDir()
	if _, err := f.Fetch(context.Background(), "SYNTHETIC-KIMI"); err != nil {
		t.Fatal(err)
	}
	if got.Get("X-Msh-Device-Id") != "" {
		t.Error("a device id was sent with none on disk")
	}
	if _, err := os.Stat(filepath.Join(f.homeDir, "device_id")); !os.IsNotExist(err) {
		t.Error("caam created a device_id file")
	}
}

func TestKimiFetcher_FetchUnauthorizedAndUserInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case KimiUsagePath:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"invalid_token","message":"token expired"}`)
		case KimiMePath:
			_, _ = io.WriteString(w, `{"userId":"u-1","nickname":"Dev","status":"ok","region":"us","userLevel":1,"userLevelName":"basic","domain":1,"domainName":"d","email":"dev@example.com"}`)
		}
	}))
	defer server.Close()
	f := NewKimiFetcher()
	f.baseURL = server.URL
	f.homeDir = t.TempDir()

	info, err := f.Fetch(context.Background(), "expired")
	if err == nil {
		t.Fatal("Fetch() returned no error on 401")
	}
	if !strings.HasPrefix(info.Error, "unauthorized") || !strings.Contains(info.Error, "token expired") || !strings.Contains(info.Error, "caam refresh kimi") {
		t.Errorf("error = %q", info.Error)
	}
	email, err := f.KimiUserInfo(context.Background(), "SYNTHETIC")
	if err != nil || email != "dev@example.com" {
		t.Fatalf("KimiUserInfo = %q, %v", email, err)
	}
}

func TestReadKimiCredentials(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kimi-code.json")
	if err := os.WriteFile(path, []byte(`{"access_token":"","refresh_token":"","expires_at":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadKimiCredentials(path); err == nil || !strings.Contains(err.Error(), "logged out") {
		t.Errorf("logged-out file = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"access_token":"SYNTHETIC-AT","refresh_token":"x","expires_at":1893456000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if tok, _, err := ReadCredentials("kimi", path); err != nil || tok != "SYNTHETIC-AT" {
		t.Errorf("ReadCredentials(kimi) = %q, %v", tok, err)
	}
	if files := CredentialFiles("kimi"); len(files) != 1 || files[0] != "kimi-code.json" {
		t.Errorf("CredentialFiles(kimi) = %v", files)
	}
}
