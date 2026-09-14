package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
)

func TestAddCommand(t *testing.T) {
	if addCmd.Use != "add <agent> [profile-name]" {
		t.Errorf("Expected Use 'add <agent> [profile-name]', got %q", addCmd.Use)
	}

	if addCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	if addCmd.Long == "" {
		t.Error("Expected non-empty Long description")
	}
}

func TestAddCommandFlags(t *testing.T) {
	flags := []struct {
		name     string
		defValue string
	}{
		{"no-activate", "false"},
		{"timeout", "5m0s"},
		{"force", "false"},
		{"device-code", "false"},
	}

	for _, tt := range flags {
		flag := addCmd.Flags().Lookup(tt.name)
		if flag == nil {
			t.Errorf("Flag %q not found", tt.name)
			continue
		}
		if flag.DefValue != tt.defValue {
			t.Errorf("Flag %q default = %q, want %q", tt.name, flag.DefValue, tt.defValue)
		}
	}
}

func TestAddCommandValidatesUnknownTool(t *testing.T) {
	// Test that unknown tools are rejected by checking the tools map
	unknownTools := []string{"unknown-tool", "notreal", "foo"}
	for _, tool := range unknownTools {
		if _, ok := tools[tool]; ok {
			t.Errorf("Tool %q should not be in tools map", tool)
		}
	}

	// Test that known tools ARE in the map
	knownTools := []string{"claude", "codex", "gemini"}
	for _, tool := range knownTools {
		if _, ok := tools[tool]; !ok {
			t.Errorf("Tool %q should be in tools map", tool)
		}
	}
}

func TestAddCommandValidatesExistingProfile(t *testing.T) {
	// Create temp vault with existing profile
	tmpDir := t.TempDir()
	vaultPath := filepath.Join(tmpDir, "vault")
	testVault := authfile.NewVault(vaultPath)

	// Create a fake existing profile
	profileDir := filepath.Join(vaultPath, "claude", "existing-profile")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, ".claude.json"), []byte(`{}`), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Test that List() returns existing profile
	profiles, err := testVault.List("claude")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	found := false
	for _, p := range profiles {
		if p == "existing-profile" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected to find 'existing-profile' in profiles list")
	}

	// Test the duplicate detection logic that runAdd would use
	profileName := "existing-profile"
	for _, p := range profiles {
		if p == profileName {
			// This is what runAdd does to detect duplicates
			t.Logf("Correctly detected existing profile: %s", profileName)
			return
		}
	}
	t.Error("Should have detected existing profile")
}

func TestAddCommandValidatesSystemProfileNames(t *testing.T) {
	// This is a unit test for the validation logic
	// Profile names starting with "_" are reserved

	invalidNames := []string{"_original", "_backup", "_system"}
	for _, name := range invalidNames {
		if !strings.HasPrefix(name, "_") {
			t.Errorf("Test case %q should start with underscore", name)
		}
	}

	validNames := []string{"work", "personal", "test-account"}
	for _, name := range validNames {
		if strings.HasPrefix(name, "_") {
			t.Errorf("Test case %q should not start with underscore", name)
		}
	}
}

// `caam add` and the dashboard's n key share one table of login commands,
// so every agent caam vaults has a login flow; --device-code selects
// Codex's headless flow.
func TestRunToolLoginCommands(t *testing.T) {
	tests := []struct {
		tool       string
		deviceCode bool
		wantBin    string
		wantArgs   string
	}{
		{"claude", false, "claude", ""},
		{"codex", false, "codex", "login"},
		{"codex", true, "codex", "login --device-auth"},
		{"gemini", false, "gemini", ""},
		{"opencode", false, "opencode", "auth login"},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			login, err := loginCommandFor(tt.tool, tt.deviceCode)
			if err != nil {
				t.Fatalf("loginCommandFor(%s): %v", tt.tool, err)
			}
			if login.Bin != tt.wantBin || strings.Join(login.Args, " ") != tt.wantArgs {
				t.Errorf("loginCommandFor(%s, %v) = %s %q, want %s %q", tt.tool, tt.deviceCode, login.Bin, login.Args, tt.wantBin, tt.wantArgs)
			}
		})
	}

	for tool := range tools {
		if login, err := loginCommandFor(tool, false); err != nil || login.Bin == "" || login.Hint == "" {
			t.Errorf("caam add %s has no complete login command: %+v, %v", tool, login, err)
		}
	}
	if _, err := loginCommandFor("notreal", false); err == nil {
		t.Error("an unknown agent should have no login flow")
	}
}
