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

// The Antigravity quota API answers with one bucket per model and token
// type; remainingFraction is what is left, and is 1.0 when absent.
const agyQuotaPayload = `{"buckets":[
  {"modelId":"gemini-3-pro","tokenType":"INPUT","remainingFraction":0.25,"resetTime":"2030-01-01T05:00:00Z"},
  {"modelId":"gemini-3-pro","tokenType":"OUTPUT","remainingFraction":0.6,"resetTime":"2030-01-01T05:00:00Z"},
  {"modelId":"gemini-3-flash","tokenType":"INPUT","remainingFraction":0.9,"resetTime":"2030-01-01T06:00:00Z"},
  {"modelId":"gemini-3-flash-lite","tokenType":"INPUT","resetTime":"2030-01-01T07:00:00Z"},
  {"tokenType":"INPUT","remainingFraction":0.1}
]}`

func TestAgyFetcher_Fetch_MapsBucketsOntoWindows(t *testing.T) {
	var gotAuth, gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, agyQuotaPayload)
	}))
	defer server.Close()

	f := NewAgyFetcher()
	f.url = server.URL
	info, err := f.Fetch(context.Background(), "SYNTHETIC-AGY")
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if gotMethod != "POST" || gotAuth != "Bearer SYNTHETIC-AGY" {
		t.Fatalf("request = %s %q, want POST with the bearer token", gotMethod, gotAuth)
	}
	if info.Provider != "agy" || info.Source != SourceAPI || info.Error != "" {
		t.Fatalf("info = %+v", info)
	}

	// The pro model is the primary window, at its most-used token type.
	if info.PrimaryWindow == nil || info.PrimaryWindow.Label != "gemini-3-pro" || info.PrimaryWindow.UsedPercent != 75 {
		t.Fatalf("primary = %+v, want gemini-3-pro at 75%%", info.PrimaryWindow)
	}
	if info.PrimaryWindow.ResetsAt.IsZero() || info.PrimaryWindow.ResetsAt.Hour() != 5 {
		t.Errorf("primary reset = %v, want the bucket's resetTime", info.PrimaryWindow.ResetsAt)
	}
	// Flash is secondary; flash-lite stays a per-model window only, with an
	// absent remainingFraction meaning untouched.
	if info.SecondaryWindow == nil || info.SecondaryWindow.Label != "gemini-3-flash" || info.SecondaryWindow.UsedPercent != 10 {
		t.Fatalf("secondary = %+v, want gemini-3-flash at 10%%", info.SecondaryWindow)
	}
	if len(info.ModelWindows) != 3 {
		t.Fatalf("ModelWindows = %d entries, want 3 (the bucket without a modelId is skipped)", len(info.ModelWindows))
	}
	if lite := info.ModelWindows["gemini-3-flash-lite"]; lite == nil || lite.UsedPercent != 0 || lite.Utilization != 0 {
		t.Errorf("flash-lite = %+v, want 0%% used", lite)
	}
	if w := info.WindowForModel("gemini-3-pro"); w == nil || w.UsedPercent != 75 {
		t.Errorf("WindowForModel(gemini-3-pro) = %+v", w)
	}
	if score := info.AvailabilityScore(); score <= 0 || score >= 100 {
		t.Errorf("AvailabilityScore = %d, want a partial score", score)
	}
}

func TestAgyFetcher_Fetch_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":401,"message":"Request had invalid authentication credentials.","status":"UNAUTHENTICATED"}}`)
	}))
	defer server.Close()

	f := NewAgyFetcher()
	f.url = server.URL
	info, err := f.Fetch(context.Background(), "expired")
	if err == nil {
		t.Fatal("Fetch() returned no error on 401")
	}
	// Google's own diagnosis travels with the error: it is what tells a
	// stale token apart from a wrong endpoint.
	if !strings.Contains(info.Error, "UNAUTHENTICATED") {
		t.Errorf("error %q lacks Google's status", info.Error)
	}
	if info == nil || !strings.HasPrefix(info.Error, "unauthorized: token expired or invalid") || !strings.Contains(info.Error, "start agy once") {
		t.Fatalf("info = %+v, want the unauthorized error with the refresh hint", info)
	}
	if _, err := f.Fetch(context.Background(), ""); err == nil {
		t.Fatal("Fetch() with an empty token must fail before any request")
	}
}

func TestReadAgyCredentials(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	// The live shape: an oauth2 token object under "token".
	nested := write("nested", `{"token":{"access_token":"SYNTHETIC-AT","token_type":"Bearer","refresh_token":"SYNTHETIC-RT","expiry":"2030-01-01T00:00:00Z"},"auth_method":"oauth"}`)
	if tok, _, err := ReadAgyCredentials(nested); err != nil || tok != "SYNTHETIC-AT" {
		t.Errorf("nested = %q, %v", tok, err)
	}
	bare := write("bare", `{"auth_method":"oauth","token":"SYNTHETIC-BARE"}`)
	if tok, _, err := ReadAgyCredentials(bare); err != nil || tok != "SYNTHETIC-BARE" {
		t.Errorf("bare token string = %q, %v", tok, err)
	}
	flat := write("flat", `{"access_token":"SYNTHETIC-FLAT","refresh_token":"x"}`)
	if tok, _, err := ReadAgyCredentials(flat); err != nil || tok != "SYNTHETIC-FLAT" {
		t.Errorf("flat = %q, %v", tok, err)
	}
	none := write("none", `{"auth_method":"oauth"}`)
	if _, _, err := ReadAgyCredentials(none); err == nil {
		t.Error("a token file with no access token must be an error")
	}
	if _, _, err := ReadAgyCredentials(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing file must be an error")
	}
	if _, _, err := ReadCredentials("agy", nested); err != nil {
		t.Errorf("ReadCredentials(agy) does not dispatch: %v", err)
	}
	if files := CredentialFiles("agy"); len(files) != 1 || files[0] != "antigravity-oauth-token" {
		t.Errorf("CredentialFiles(agy) = %v", files)
	}
}

func TestAgyUserInfo(t *testing.T) {
	var gotAuth, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"sub":"1","email":"chris@example.com","email_verified":true}`)
	}))
	defer server.Close()

	f := NewAgyFetcher()
	f.userInfoURL = server.URL
	email, err := f.AgyUserInfo(context.Background(), "SYNTHETIC")
	if err != nil {
		t.Fatalf("AgyUserInfo: %v", err)
	}
	if email != "chris@example.com" {
		t.Errorf("email = %q", email)
	}
	// The token goes in the header, never the query string.
	if gotAuth != "Bearer SYNTHETIC" || gotQuery != "" {
		t.Errorf("request auth=%q query=%q", gotAuth, gotQuery)
	}

	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"Invalid Credentials","status":"UNAUTHENTICATED"}}`)
	}))
	defer denied.Close()
	f.userInfoURL = denied.URL
	if _, err := f.AgyUserInfo(context.Background(), "expired"); err == nil || !strings.Contains(err.Error(), "UNAUTHENTICATED") {
		t.Errorf("denied userinfo = %v, want an error carrying Google's status", err)
	}
}
