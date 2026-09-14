package api

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
)

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     string
	}{
		{30 * time.Second, "<1m"},
		{1 * time.Minute, "1m"},
		{5 * time.Minute, "5m"},
		{59 * time.Minute, "59m"},
		{1 * time.Hour, "1h 0m"},
		{1*time.Hour + 30*time.Minute, "1h 30m"},
		{2 * time.Hour, "2h 0m"},
		{2*time.Hour + 45*time.Minute, "2h 45m"},
	}

	for _, tt := range tests {
		t.Run(tt.duration.String(), func(t *testing.T) {
			got := formatDuration(tt.duration)
			if got != tt.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.duration, got, tt.want)
			}
		})
	}
}

func TestNewHandlers(t *testing.T) {
	// Test with nil dependencies
	h := NewHandlers(nil, nil, nil)
	if h == nil {
		t.Fatal("NewHandlers() returned nil")
	}
}

func TestGetStatusWithNilDeps(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	// Should not panic with nil vault
	// Note: This will return empty tools since vault is nil
	status, err := h.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus() error = %v", err)
	}
	if status == nil {
		t.Fatal("GetStatus() returned nil")
	}
	if status.Version == "" {
		t.Error("GetStatus() version is empty")
	}
	if status.Timestamp == "" {
		t.Error("GetStatus() timestamp is empty")
	}
}

func TestGetProfilesWithNilVault(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	// With nil vault, should return error when trying to list
	_, err := h.GetProfiles("")
	if err == nil {
		t.Error("GetProfiles() expected error with nil vault")
	}
}

func TestGetProfilesWithUnknownTool(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	_, err := h.GetProfiles("unknown-tool")
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("GetProfiles() expected unknown tool error, got %v", err)
	}
}

// TestAPIUsage_LastUsedIsFromTheActivityLog: /usage reported the health
// probe's time as last_used. The activity log says when an account was
// last used; the probe time is last_checked. Every tool caam manages is
// listed, not three.
func TestAPIUsage_LastUsedIsFromTheActivityLog(t *testing.T) {
	dir := t.TempDir()
	vault := authfile.NewVault(filepath.Join(dir, "vault"))
	for _, p := range [][2]string{{"codex", "work"}, {"kimi", "k1"}} {
		if err := os.MkdirAll(vault.ProfilePath(p[0], p[1]), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	checked := time.Date(2026, 9, 13, 17, 0, 0, 0, time.UTC)
	store := health.NewStorage(filepath.Join(dir, "health.json"))
	for _, p := range [][2]string{{"codex", "work"}, {"kimi", "k1"}} {
		if err := store.UpdateProfile(p[0], p[1], &health.ProfileHealth{LastChecked: checked}); err != nil {
			t.Fatal(err)
		}
	}

	used := checked.Add(-3 * time.Hour)
	db, err := caamdb.OpenAt(filepath.Join(dir, "caam.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.LogEvent(caamdb.Event{Type: caamdb.EventActivate, Provider: "codex", ProfileName: "work", Timestamp: used}); err != nil {
		t.Fatal(err)
	}

	resp, err := NewHandlers(vault, store, db).GetUsage("")
	if err != nil {
		t.Fatalf("GetUsage: %v", err)
	}
	byKey := map[string]UsageEntry{}
	for _, e := range resp.Usage {
		byKey[e.Tool+"/"+e.Profile] = e
	}
	work, ok := byKey["codex/work"]
	if !ok {
		t.Fatalf("codex/work missing from %+v", resp.Usage)
	}
	if work.LastUsed != used.Format(time.RFC3339) {
		t.Errorf("last_used = %q, want the activity log's %s", work.LastUsed, used.Format(time.RFC3339))
	}
	if work.LastChecked != checked.Format(time.RFC3339) {
		t.Errorf("last_checked = %q, want the probe's %s", work.LastChecked, checked.Format(time.RFC3339))
	}
	k1, ok := byKey["kimi/k1"]
	if !ok {
		t.Fatalf("kimi/k1 missing: /usage must list every tool, got %+v", resp.Usage)
	}
	if k1.LastUsed != "" {
		t.Errorf("an account the log never saw has no last_used, got %q", k1.LastUsed)
	}
}

func TestGetUsageWithNilDeps(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	// Should return empty usage without error
	usage, err := h.GetUsage("")
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}
	if usage == nil {
		t.Fatal("GetUsage() returned nil")
	}
	if len(usage.Usage) != 0 {
		t.Errorf("GetUsage() with nil deps should return empty, got %d entries", len(usage.Usage))
	}
}

func TestGetCoordinators(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	// Should return empty coordinators list
	coords, err := h.GetCoordinators()
	if err != nil {
		t.Fatalf("GetCoordinators() error = %v", err)
	}
	if coords == nil {
		t.Fatal("GetCoordinators() returned nil")
	}
	if coords.Coordinators == nil {
		t.Error("GetCoordinators() coordinators list is nil")
	}
}

func TestActivateWithUnknownTool(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	req := ActivateRequest{
		Tool:    "unknown",
		Profile: "test",
	}

	_, err := h.Activate(req)
	if err == nil {
		t.Error("Activate() expected error for unknown tool")
	}
}

func TestActivateWithMissingProfile(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	req := ActivateRequest{
		Tool:    "codex",
		Profile: "",
	}

	_, err := h.Activate(req)
	if err == nil || !strings.Contains(err.Error(), "profile is required") {
		t.Errorf("Activate() expected profile required error, got %v", err)
	}
}

func TestBackupWithUnknownTool(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	req := BackupRequest{
		Tool:    "unknown",
		Profile: "test",
	}

	_, err := h.Backup(req)
	if err == nil {
		t.Error("Backup() expected error for unknown tool")
	}
}

func TestBackupWithMissingProfile(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	req := BackupRequest{
		Tool:    "codex",
		Profile: "",
	}

	_, err := h.Backup(req)
	if err == nil || !strings.Contains(err.Error(), "profile is required") {
		t.Errorf("Backup() expected profile required error, got %v", err)
	}
}

func TestDeleteProfileWithUnknownTool(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	err := h.DeleteProfile("unknown", "test")
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("DeleteProfile() expected unknown tool error, got %v", err)
	}
}

func TestDeleteProfileWithMissingProfile(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	err := h.DeleteProfile("codex", "")
	if err == nil || !strings.Contains(err.Error(), "profile is required") {
		t.Errorf("DeleteProfile() expected profile required error, got %v", err)
	}
}

func TestDeleteProfileWithNilVault(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	err := h.DeleteProfile("codex", "test")
	if err == nil {
		t.Error("DeleteProfile() expected error with nil vault")
	}
}

func TestGetProfileWithUnknownTool(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	_, err := h.GetProfile("unknown-tool", "test")
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("GetProfile() expected unknown tool error, got %v", err)
	}
}

func TestGetProfileWithNilVault(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	_, err := h.GetProfile("codex", "test")
	if err == nil {
		t.Error("GetProfile() expected error with nil vault")
	}
}

func TestGetProfileHealthWithNilStore(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	// Should return nil without panic
	health := h.getProfileHealth("claude", "test")
	if health != nil {
		t.Errorf("getProfileHealth() with nil store should return nil, got %v", health)
	}
}

func TestGetProfileIdentityWithNilVault(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	// Should return nil without panic
	id := h.getProfileIdentity("claude", "test")
	if id != nil {
		t.Errorf("getProfileIdentity() with nil vault should return nil, got %v", id)
	}
}

func TestToolsMapContainsExpectedTools(t *testing.T) {
	expectedTools := []string{"codex", "claude", "gemini"}

	for _, tool := range expectedTools {
		if _, ok := tools[tool]; !ok {
			t.Errorf("tools map missing %q", tool)
		}
	}
}

// POST /actions/activate switches through the shared core: the signed-in
// account is re-captured first, and a failed re-capture is an error, not a
// switch.
func TestAPIActivate_RecapturesBeforeRestoring(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	tmp := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(tmp, "codex_home"))
	if err := os.MkdirAll(os.Getenv("CODEX_HOME"), 0o700); err != nil {
		t.Fatal(err)
	}
	vault := authfile.NewVault(filepath.Join(tmp, "vault"))
	write := func(path string, data []byte) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC).Unix()
	stale := syntheticCodexAuth(t, "a@example.com", "a-stale", base)
	rotated := syntheticCodexAuth(t, "a@example.com", "a-rotated", base+3600)
	incoming := syntheticCodexAuth(t, "b@example.com", "b", base)
	write(filepath.Join(vault.ProfilePath("codex", "a"), "auth.json"), stale)
	write(filepath.Join(vault.ProfilePath("codex", "b"), "auth.json"), incoming)
	livePath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	write(livePath, rotated)

	h := NewHandlers(vault, nil, nil)
	resp, err := h.Activate(ActivateRequest{Tool: "codex", Profile: "b"})
	if err != nil || !resp.Success {
		t.Fatalf("Activate() = %+v, %v", resp, err)
	}
	gotA, _ := os.ReadFile(filepath.Join(vault.ProfilePath("codex", "a"), "auth.json"))
	if string(gotA) != string(rotated) {
		t.Fatalf("outgoing a was not re-captured before the switch; vault holds %s", gotA)
	}
	if gotLive, _ := os.ReadFile(livePath); string(gotLive) != string(incoming) {
		t.Fatalf("live credential = %s, want b", gotLive)
	}

	// Switch back with b un-recapturable: refused, live untouched.
	bDir := vault.ProfilePath("codex", "b")
	if err := os.Chmod(bDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bDir, 0o700) })
	write(livePath, syntheticCodexAuth(t, "b@example.com", "b-rotated", base+7200))
	if _, err := h.Activate(ActivateRequest{Tool: "codex", Profile: "a"}); err == nil {
		t.Fatal("Activate() should refuse when the outgoing account cannot be re-captured")
	}
	if gotLive, _ := os.ReadFile(livePath); !strings.Contains(string(gotLive), "b-rotated") {
		t.Fatalf("live credential was replaced despite the refusal: %s", gotLive)
	}
}

// syntheticCodexAuth builds a ChatGPT-mode Codex auth.json whose id_token
// names email; unsigned and synthetic.
func syntheticCodexAuth(t *testing.T, email, tag string, issuedAt int64) []byte {
	t.Helper()
	jwt := func(claims map[string]any) string {
		payload, err := json.Marshal(claims)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
			base64.RawURLEncoding.EncodeToString(payload) + ".sig"
	}
	auth := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      jwt(map[string]any{"email": email, "iat": issuedAt, "exp": issuedAt + 3600}),
			"access_token":  jwt(map[string]any{"sub": email, "iat": issuedAt, "exp": issuedAt + 3600}),
			"refresh_token": "SYNTHETIC-REFRESH-" + tag,
		},
		"last_refresh": time.Unix(issuedAt, 0).UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
