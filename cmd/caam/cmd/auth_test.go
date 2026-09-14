// Package cmd implements the CLI commands for caam.
package cmd

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/bundle"
)

// =============================================================================
// Auth Command Structure Tests
// =============================================================================

func TestAuthCommandStructure(t *testing.T) {
	if authCmd.Use != "auth" {
		t.Errorf("Expected Use 'auth', got %q", authCmd.Use)
	}

	if authCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	if authCmd.Long == "" {
		t.Error("Expected non-empty Long description")
	}
}

func TestAuthSubcommands(t *testing.T) {
	subcommands := authCmd.Commands()

	expectedCmds := map[string]bool{
		"detect": false,
	}

	for _, cmd := range subcommands {
		parts := strings.Fields(cmd.Use)
		if len(parts) > 0 {
			expectedCmds[parts[0]] = true
		}
	}

	for name, found := range expectedCmds {
		if !found {
			t.Errorf("Expected subcommand %q not found", name)
		}
	}
}

// =============================================================================
// Auth Detect Command Tests
// =============================================================================

func TestAuthDetectCommandStructure(t *testing.T) {
	if !strings.HasPrefix(authDetectCmd.Use, "detect") {
		t.Errorf("Expected Use to start with 'detect', got %q", authDetectCmd.Use)
	}

	if authDetectCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	if authDetectCmd.Long == "" {
		t.Error("Expected non-empty Long description")
	}
}

func TestAuthDetectFlags(t *testing.T) {
	// Check --json flag exists
	flag := authDetectCmd.Flags().Lookup("json")
	if flag == nil {
		t.Error("Expected --json flag")
	}
	if flag.DefValue != "false" {
		t.Errorf("Expected default false, got %q", flag.DefValue)
	}
}

func TestAuthDetectArgs(t *testing.T) {
	// Test arg validation - accepts 0 or 1 arg
	err := authDetectCmd.Args(authDetectCmd, []string{})
	if err != nil {
		t.Errorf("Expected no error for 0 args: %v", err)
	}

	err = authDetectCmd.Args(authDetectCmd, []string{"claude"})
	if err != nil {
		t.Errorf("Expected no error for 1 arg: %v", err)
	}

	err = authDetectCmd.Args(authDetectCmd, []string{"claude", "codex"})
	if err == nil {
		t.Error("Expected error for 2 args")
	}
}

// =============================================================================
// AuthDetectReport Tests
// =============================================================================

func TestAuthDetectReportStructure(t *testing.T) {
	report := &AuthDetectReport{
		Timestamp: time.Now().Format(time.RFC3339),
		Results: []AuthDetectResult{
			{
				Provider: "claude",
				Found:    true,
				Locations: []AuthDetectLocation{
					{
						Path:         "/home/user/.claude.json",
						Exists:       true,
						LastModified: time.Now().Format(time.RFC3339),
						FileSize:     256,
						IsValid:      true,
						Description:  "Claude CLI OAuth credentials",
					},
				},
			},
			{
				Provider:  "codex",
				Found:     false,
				Locations: []AuthDetectLocation{},
			},
		},
		Summary: AuthDetectSummary{
			TotalProviders: 2,
			FoundCount:     1,
			NotFoundCount:  1,
		},
	}

	// Verify report fields
	if report.Timestamp == "" {
		t.Error("Timestamp should not be empty")
	}

	if len(report.Results) != 2 {
		t.Errorf("Expected 2 results, got %d", len(report.Results))
	}

	if report.Summary.TotalProviders != 2 {
		t.Errorf("Expected TotalProviders 2, got %d", report.Summary.TotalProviders)
	}

	if report.Summary.FoundCount != 1 {
		t.Errorf("Expected FoundCount 1, got %d", report.Summary.FoundCount)
	}

	if report.Summary.NotFoundCount != 1 {
		t.Errorf("Expected NotFoundCount 1, got %d", report.Summary.NotFoundCount)
	}
}

func TestAuthDetectReportJSON(t *testing.T) {
	report := &AuthDetectReport{
		Timestamp: "2024-01-15T10:30:00Z",
		Results: []AuthDetectResult{
			{
				Provider: "claude",
				Found:    true,
				Locations: []AuthDetectLocation{
					{
						Path:         "/home/user/.claude.json",
						Exists:       true,
						LastModified: "2024-01-14T15:00:00Z",
						FileSize:     256,
						IsValid:      true,
						Description:  "Claude CLI OAuth credentials",
					},
				},
				Primary: &AuthDetectLocation{
					Path:         "/home/user/.claude.json",
					Exists:       true,
					LastModified: "2024-01-14T15:00:00Z",
					FileSize:     256,
					IsValid:      true,
					Description:  "Claude CLI OAuth credentials",
				},
			},
		},
		Summary: AuthDetectSummary{
			TotalProviders: 1,
			FoundCount:     1,
			NotFoundCount:  0,
		},
	}

	// Test JSON marshaling
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("JSON marshaling failed: %v", err)
	}

	jsonStr := string(data)

	// Verify key fields are present
	if !strings.Contains(jsonStr, `"provider": "claude"`) {
		t.Error("JSON should contain provider field")
	}

	if !strings.Contains(jsonStr, `"found": true`) {
		t.Error("JSON should contain found field")
	}

	if !strings.Contains(jsonStr, `"path": "/home/user/.claude.json"`) {
		t.Error("JSON should contain path field")
	}

	// Test JSON unmarshaling
	var decoded AuthDetectReport
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("JSON unmarshaling failed: %v", err)
	}

	if decoded.Timestamp != report.Timestamp {
		t.Errorf("Decoded Timestamp = %q, want %q", decoded.Timestamp, report.Timestamp)
	}

	if len(decoded.Results) != 1 {
		t.Errorf("Decoded Results len = %d, want 1", len(decoded.Results))
	}

	if decoded.Results[0].Provider != "claude" {
		t.Errorf("Decoded Provider = %q, want %q", decoded.Results[0].Provider, "claude")
	}
}

func TestAuthDetectResultWithError(t *testing.T) {
	result := AuthDetectResult{
		Provider:  "gemini",
		Found:     false,
		Locations: []AuthDetectLocation{},
		Error:     "failed to access home directory",
	}

	// Verify error is serialized
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("JSON marshaling failed: %v", err)
	}

	if !strings.Contains(string(data), `"error":"failed to access home directory"`) {
		t.Error("JSON should contain error field")
	}
}

func TestAuthDetectResultWithWarning(t *testing.T) {
	result := AuthDetectResult{
		Provider: "gemini",
		Found:    true,
		Locations: []AuthDetectLocation{
			{Path: "/home/user/.gemini/settings.json", Exists: true, IsValid: true},
			{Path: "/home/user/.config/gemini/settings.json", Exists: true, IsValid: true},
		},
		Warning: "Multiple auth files found",
	}

	// Verify warning is serialized
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("JSON marshaling failed: %v", err)
	}

	if !strings.Contains(string(data), `"warning":"Multiple auth files found"`) {
		t.Error("JSON should contain warning field")
	}
}

// =============================================================================
// AuthDetectLocation Tests
// =============================================================================

func TestAuthDetectLocationFields(t *testing.T) {
	loc := AuthDetectLocation{
		Path:            "/home/user/.codex/auth.json",
		Exists:          true,
		LastModified:    "2024-01-15T12:00:00Z",
		FileSize:        512,
		IsValid:         true,
		ValidationError: "",
		Description:     "Codex CLI OAuth token",
	}

	if loc.Path != "/home/user/.codex/auth.json" {
		t.Errorf("Path = %q, want %q", loc.Path, "/home/user/.codex/auth.json")
	}

	if !loc.Exists {
		t.Error("Exists should be true")
	}

	if !loc.IsValid {
		t.Error("IsValid should be true")
	}

	if loc.ValidationError != "" {
		t.Errorf("ValidationError should be empty, got %q", loc.ValidationError)
	}

	if loc.FileSize != 512 {
		t.Errorf("FileSize = %d, want 512", loc.FileSize)
	}
}

func TestAuthDetectLocationInvalid(t *testing.T) {
	loc := AuthDetectLocation{
		Path:            "/home/user/.claude.json",
		Exists:          true,
		FileSize:        10,
		IsValid:         false,
		ValidationError: "invalid JSON format",
		Description:     "Claude CLI OAuth credentials",
	}

	if loc.IsValid {
		t.Error("IsValid should be false")
	}

	if loc.ValidationError == "" {
		t.Error("ValidationError should not be empty when IsValid is false")
	}
}

// =============================================================================
// Helper Function Tests
// =============================================================================

func TestShortenPath(t *testing.T) {
	// Save original env lookup
	originalEnvLookup := envLookup
	defer func() { envLookup = originalEnvLookup }()

	// Mock HOME environment
	envLookup = func(key string) string {
		if key == "HOME" {
			return "/home/testuser"
		}
		return ""
	}

	tests := []struct {
		input string
		want  string
	}{
		{"/home/testuser/.claude.json", "~/.claude.json"},
		{"/home/testuser/.codex/auth.json", "~/.codex/auth.json"},
		{"/other/path/.config", "/other/path/.config"},
		{"/home/otheruser/.claude.json", "/home/otheruser/.claude.json"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := shortenPath(tt.input)
			if got != tt.want {
				t.Errorf("shortenPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFormatFileSize(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{100, "100 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1572864, "1.5 MB"},
		{1073741824, "1.0 GB"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := bundle.FormatSize(tt.bytes)
			if got != tt.want {
				t.Errorf("bundle.FormatSize(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}

func TestGetProviderDisplayName(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		{"claude", "Claude Code"},
		{"codex", "Codex"},
		{"gemini", "Gemini"},
		{"agy", "Antigravity"},
		{"unknown", "Unknown"}, // Falls back to title case
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			got := getProviderDisplayName(tt.id)
			if got != tt.want {
				t.Errorf("getProviderDisplayName(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestGetHomeDir(t *testing.T) {
	// Save original env lookup
	originalEnvLookup := envLookup
	defer func() { envLookup = originalEnvLookup }()

	t.Run("HOME set", func(t *testing.T) {
		envLookup = func(key string) string {
			if key == "HOME" {
				return "/home/testuser"
			}
			return ""
		}

		home, err := getHomeDir()
		if err != nil {
			t.Fatalf("getHomeDir() error = %v", err)
		}
		if home != "/home/testuser" {
			t.Errorf("getHomeDir() = %q, want %q", home, "/home/testuser")
		}
	})

	t.Run("USERPROFILE set (Windows)", func(t *testing.T) {
		envLookup = func(key string) string {
			if key == "USERPROFILE" {
				return "C:\\Users\\testuser"
			}
			return ""
		}

		home, err := getHomeDir()
		if err != nil {
			t.Fatalf("getHomeDir() error = %v", err)
		}
		if home != "C:\\Users\\testuser" {
			t.Errorf("getHomeDir() = %q, want %q", home, "C:\\Users\\testuser")
		}
	})

	t.Run("neither set", func(t *testing.T) {
		envLookup = func(key string) string {
			return ""
		}

		_, err := getHomeDir()
		if err == nil {
			t.Error("getHomeDir() should error when no home found")
		}
	})
}

// =============================================================================
// AuthDetectSummary Tests
// =============================================================================

func TestAuthDetectSummary(t *testing.T) {
	summary := AuthDetectSummary{
		TotalProviders: 3,
		FoundCount:     2,
		NotFoundCount:  1,
	}

	if summary.TotalProviders != 3 {
		t.Errorf("TotalProviders = %d, want 3", summary.TotalProviders)
	}

	if summary.FoundCount != 2 {
		t.Errorf("FoundCount = %d, want 2", summary.FoundCount)
	}

	if summary.NotFoundCount != 1 {
		t.Errorf("NotFoundCount = %d, want 1", summary.NotFoundCount)
	}

	// Verify totals match
	if summary.FoundCount+summary.NotFoundCount != summary.TotalProviders {
		t.Error("FoundCount + NotFoundCount should equal TotalProviders")
	}
}

func TestAuthDetectSummaryJSON(t *testing.T) {
	summary := AuthDetectSummary{
		TotalProviders: 3,
		FoundCount:     2,
		NotFoundCount:  1,
	}

	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("JSON marshaling failed: %v", err)
	}

	if !strings.Contains(string(data), `"total_providers":3`) {
		t.Error("JSON should contain total_providers field")
	}

	if !strings.Contains(string(data), `"found_count":2`) {
		t.Error("JSON should contain found_count field")
	}

	if !strings.Contains(string(data), `"not_found_count":1`) {
		t.Error("JSON should contain not_found_count field")
	}
}
