package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
)

func TestRefreshSingle_CodexUpdatesAuth(t *testing.T) {
	tmpDir := t.TempDir()

	// Keep SPM config reads inside tmpDir.
	oldCaamHome := os.Getenv("CAAM_HOME")
	t.Cleanup(func() { _ = os.Setenv("CAAM_HOME", oldCaamHome) })
	_ = os.Setenv("CAAM_HOME", tmpDir)

	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	profileDir := vault.ProfilePath("codex", "main")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	original := map[string]any{
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
		"expires_at":    time.Now().Add(-time.Minute).Unix(),
		"token_type":    "Bearer",
	}
	raw, err := json.MarshalIndent(original, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	authPath := filepath.Join(profileDir, "auth.json")
	if err := os.WriteFile(authPath, raw, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600,"token_type":"Bearer"}`))
	}))
	defer ts.Close()

	oldTokenURL := refresh.CodexTokenURL
	refresh.CodexTokenURL = ts.URL
	t.Cleanup(func() { refresh.CodexTokenURL = oldTokenURL })

	if err := refreshSingle(context.Background(), "codex", "main", false, false, true); err != nil {
		t.Fatalf("refreshSingle() error = %v", err)
	}

	updatedRaw, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var updated map[string]any
	if err := json.Unmarshal(updatedRaw, &updated); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if got := updated["access_token"]; got != "new-access" {
		t.Fatalf("access_token = %v, want %v", got, "new-access")
	}
	if got := updated["refresh_token"]; got != "new-refresh" {
		t.Fatalf("refresh_token = %v, want %v", got, "new-refresh")
	}
	if got, ok := updated["expires_at"].(float64); !ok || got <= float64(original["expires_at"].(int64)) {
		t.Fatalf("expires_at not updated: %v", updated["expires_at"])
	}
}

func TestRefreshSingle_SkipsWhenNotExpiring(t *testing.T) {
	tmpDir := t.TempDir()

	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	profileDir := vault.ProfilePath("codex", "main")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	original := map[string]any{
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
		"expires_at":    time.Now().Add(2 * time.Hour).Unix(),
		"token_type":    "Bearer",
	}
	raw, err := json.MarshalIndent(original, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	authPath := filepath.Join(profileDir, "auth.json")
	if err := os.WriteFile(authPath, raw, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := refreshSingle(context.Background(), "codex", "main", false, false, true); err != nil {
		t.Fatalf("refreshSingle() error = %v", err)
	}

	updatedRaw, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(updatedRaw) != string(raw) {
		t.Fatalf("auth.json changed but should have been skipped")
	}
}

func TestRefreshSingle_SkipsWhenUnsupported(t *testing.T) {
	tmpDir := t.TempDir()

	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	profileDir := vault.ProfilePath("gemini", "main")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	original := map[string]any{
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
		"expiry":        time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		"token_type":    "Bearer",
	}
	raw, err := json.MarshalIndent(original, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}

	settingsPath := filepath.Join(profileDir, "settings.json")
	if err := os.WriteFile(settingsPath, raw, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// No oauth_creds.json present, so refresh should be treated as unsupported and skipped.
	if err := refreshSingle(context.Background(), "gemini", "main", false, false, true); err != nil {
		t.Fatalf("refreshSingle() error = %v", err)
	}

	updatedRaw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var updated map[string]any
	if err := json.Unmarshal(updatedRaw, &updated); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got := updated["access_token"]; got != "old-access" {
		t.Fatalf("access_token = %v, want %v", got, "old-access")
	}
}

func TestRefreshSingle_GeminiUpdatesSettings(t *testing.T) {
	tmpDir := t.TempDir()

	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	oldHealthStore := healthStore
	healthStore = health.NewStorage(filepath.Join(tmpDir, "health.json"))
	t.Cleanup(func() { healthStore = oldHealthStore })

	profileDir := vault.ProfilePath("gemini", "main")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	settings := map[string]any{
		"access_token":  "old-access",
		"refresh_token": "ignored-refresh-token",
		"expiry":        time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		"token_type":    "Bearer",
	}
	settingsRaw, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	settingsPath := filepath.Join(profileDir, "settings.json")
	if err := os.WriteFile(settingsPath, settingsRaw, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	adc := map[string]any{
		"client_id":     "test-client",
		"client_secret": "test-secret",
		"refresh_token": "test-refresh",
		"type":          "authorized_user",
	}
	adcRaw, err := json.MarshalIndent(adc, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	oauthCredPath := filepath.Join(profileDir, "oauth_creds.json")
	if err := os.WriteFile(oauthCredPath, adcRaw, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","expires_in":3600,"token_type":"Bearer"}`))
	}))
	defer ts.Close()

	oldTokenURL := refresh.GeminiTokenURL
	refresh.GeminiTokenURL = ts.URL
	t.Cleanup(func() { refresh.GeminiTokenURL = oldTokenURL })

	if err := refreshSingle(context.Background(), "gemini", "main", false, false, true); err != nil {
		t.Fatalf("refreshSingle() error = %v", err)
	}

	updatedRaw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var updated map[string]any
	if err := json.Unmarshal(updatedRaw, &updated); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got := updated["access_token"]; got != "new-access" {
		t.Fatalf("access_token = %v, want %v", got, "new-access")
	}
	if got := updated["expiry"]; got == "" {
		t.Fatalf("expiry is empty")
	}
}

// TestRefreshSingle_ClaudeReturnsUnsupported verifies that Claude refresh is correctly
// disabled and returns a graceful skip (not an error) when attempted.
// This is a regression test for caam-tfzr.1 / CLAUDE-006.
// See: docs/CLAUDE_AUTH_INVENTORY.md
func TestRefreshSingle_ClaudeReturnsUnsupported(t *testing.T) {
	tmpDir := t.TempDir()

	// Setup temp CAAM_HOME to avoid affecting real config
	oldCaamHome := os.Getenv("CAAM_HOME")
	t.Cleanup(func() { _ = os.Setenv("CAAM_HOME", oldCaamHome) })
	_ = os.Setenv("CAAM_HOME", tmpDir)

	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	oldHealthStore := healthStore
	healthStore = health.NewStorage(filepath.Join(tmpDir, "health.json"))
	t.Cleanup(func() { healthStore = oldHealthStore })

	profileDir := vault.ProfilePath("claude", "test-profile")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	// Create a Claude credentials file with short expiry to trigger refresh attempt
	credentials := map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":      "sk-ant-oat01-test-opaque-token",
			"refreshToken":     "sk-ant-ort01-test-refresh-token",
			"expiresAt":        time.Now().Add(-time.Minute).UnixMilli(),
			"subscriptionType": "claude_pro_2025",
		},
	}
	credRaw, err := json.MarshalIndent(credentials, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	credPath := filepath.Join(profileDir, ".credentials.json")
	if err := os.WriteFile(credPath, credRaw, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// Attempt refresh - should return nil (graceful skip) not an error
	// because Claude refresh is disabled and handled via ErrUnsupported
	err = refreshSingle(context.Background(), "claude", "test-profile", false, false, true)

	// Claude refresh should NOT return an error - it's skipped gracefully
	if err != nil {
		t.Fatalf("refreshSingle() should return nil for unsupported Claude refresh, got error = %v", err)
	}

	// Verify the file wasn't modified (no refresh occurred)
	afterRaw, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var afterCreds map[string]any
	if err := json.Unmarshal(afterRaw, &afterCreds); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	oauth, ok := afterCreds["claudeAiOauth"].(map[string]any)
	if !ok {
		t.Fatal("claudeAiOauth not found after refresh attempt")
	}

	// Access token should be unchanged (no refresh occurred)
	if got := oauth["accessToken"]; got != "sk-ant-oat01-test-opaque-token" {
		t.Errorf("accessToken was modified unexpectedly: got %v", got)
	}
}

// caam refresh kimi <account>: an expired Kimi token in the vault is
// refreshed through Kimi's token endpoint and the vault copy rewritten;
// one with time left is skipped, because a refresh spends the refresh
// token.
func TestRefreshSingle_KimiUpdatesTheVaultToken(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CAAM_HOME", tmpDir)
	t.Setenv("KIMI_CODE_HOME", filepath.Join(tmpDir, "kimi-home"))
	t.Setenv("KIMI_CODE_OAUTH_HOST", "")
	t.Setenv("KIMI_OAUTH_HOST", "")

	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	profileDir := vault.ProfilePath("kimi", "work")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	authPath := filepath.Join(profileDir, "kimi-code.json")
	writeKimi := func(expiresAt int64) {
		t.Helper()
		raw, err := json.MarshalIndent(map[string]any{
			"access_token":  "old-access",
			"refresh_token": "old-refresh",
			"expires_at":    expiresAt,
			"expires_in":    3600,
			"scope":         "kimi-code",
			"token_type":    "Bearer",
		}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(authPath, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}

	requests := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if err := r.ParseForm(); err != nil || r.PostForm.Get("refresh_token") != "old-refresh" {
			t.Errorf("form = %v (%v)", r.PostForm, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600,"token_type":"Bearer"}`))
	}))
	defer ts.Close()
	oldTokenURL := refresh.KimiTokenURL
	refresh.KimiTokenURL = ts.URL + refresh.KimiTokenPath
	t.Cleanup(func() { refresh.KimiTokenURL = oldTokenURL })

	// Time left: skipped, nothing spent.
	writeKimi(time.Now().Add(2 * time.Hour).Unix())
	if err := refreshSingle(context.Background(), "kimi", "work", false, false, true); err != nil {
		t.Fatalf("refreshSingle() (fresh) error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("a token with time left was refreshed (%d requests)", requests)
	}

	// Expired: refreshed, the vault copy rewritten.
	writeKimi(time.Now().Add(-time.Minute).Unix())
	if err := refreshSingle(context.Background(), "kimi", "work", false, false, true); err != nil {
		t.Fatalf("refreshSingle() (expired) error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	updatedRaw, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var updated map[string]any
	if err := json.Unmarshal(updatedRaw, &updated); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if updated["access_token"] != "new-access" || updated["refresh_token"] != "new-refresh" || updated["scope"] != "kimi-code" {
		t.Fatalf("vault copy = %v", updated)
	}
	if got, ok := updated["expires_at"].(float64); !ok || int64(got) <= time.Now().Unix() {
		t.Fatalf("expires_at not moved into the future: %v", updated["expires_at"])
	}
	if info, err := loadExpiryInfo("kimi", "work"); err != nil || info == nil || !info.HasRefreshToken || info.ExpiresAt.Before(time.Now()) {
		t.Fatalf("loadExpiryInfo(kimi) = %+v, %v", info, err)
	}
}
