package refresh

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
)

// =============================================================================
// ShouldRefresh Tests
// =============================================================================

// =============================================================================
// getRefreshTokenFromJSON Tests
// =============================================================================

func TestGetRefreshToken_SnakeCase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	content := map[string]interface{}{
		"access_token":  "test-access-token",
		"refresh_token": "test-refresh-token-snake",
		"token_type":    "Bearer",
	}
	writeJSON(t, path, content)

	token, err := getRefreshTokenFromJSON(path)
	if err != nil {
		t.Fatalf("getRefreshTokenFromJSON error: %v", err)
	}
	if token != "test-refresh-token-snake" {
		t.Errorf("token = %q, want %q", token, "test-refresh-token-snake")
	}
}

func TestGetRefreshToken_CamelCase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	content := map[string]interface{}{
		"accessToken":  "test-access-token",
		"refreshToken": "test-refresh-token-camel",
		"tokenType":    "Bearer",
	}
	writeJSON(t, path, content)

	token, err := getRefreshTokenFromJSON(path)
	if err != nil {
		t.Fatalf("getRefreshTokenFromJSON error: %v", err)
	}
	if token != "test-refresh-token-camel" {
		t.Errorf("token = %q, want %q", token, "test-refresh-token-camel")
	}
}

func TestGetRefreshToken_SnakeCasePreferredOverCamelCase(t *testing.T) {
	// When both are present, snake_case should be preferred
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	content := map[string]interface{}{
		"refresh_token": "snake-wins",
		"refreshToken":  "camel-loses",
	}
	writeJSON(t, path, content)

	token, err := getRefreshTokenFromJSON(path)
	if err != nil {
		t.Fatalf("getRefreshTokenFromJSON error: %v", err)
	}
	if token != "snake-wins" {
		t.Errorf("token = %q, want %q (snake_case should be preferred)", token, "snake-wins")
	}
}

func TestGetRefreshToken_Missing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	content := map[string]interface{}{
		"access_token": "test-access-token",
		"token_type":   "Bearer",
		// No refresh_token or refreshToken
	}
	writeJSON(t, path, content)

	_, err := getRefreshTokenFromJSON(path)
	if err == nil {
		t.Error("getRefreshTokenFromJSON should error when refresh_token is missing")
	}
}

func TestGetRefreshToken_EmptyToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	content := map[string]interface{}{
		"access_token":  "test-access-token",
		"refresh_token": "", // Empty string
	}
	writeJSON(t, path, content)

	_, err := getRefreshTokenFromJSON(path)
	if err == nil {
		t.Error("getRefreshTokenFromJSON should error when refresh_token is empty")
	}
}

func TestGetRefreshToken_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	// Write invalid JSON
	if err := os.WriteFile(path, []byte("{not valid json"), 0600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	_, err := getRefreshTokenFromJSON(path)
	if err == nil {
		t.Error("getRefreshTokenFromJSON should error on invalid JSON")
	}
}

func TestGetRefreshToken_MissingFile(t *testing.T) {
	_, err := getRefreshTokenFromJSON("/nonexistent/path/auth.json")
	if err == nil {
		t.Error("getRefreshTokenFromJSON should error when file doesn't exist")
	}
	if !os.IsNotExist(err) {
		t.Logf("Note: error is not os.IsNotExist, got: %v", err)
	}
}

func TestGetRefreshToken_NonStringToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	content := map[string]interface{}{
		"access_token":  "test-access-token",
		"refresh_token": 12345, // Number instead of string
	}
	writeJSON(t, path, content)

	_, err := getRefreshTokenFromJSON(path)
	if err == nil {
		t.Error("getRefreshTokenFromJSON should error when refresh_token is not a string")
	}
}

// =============================================================================
// readNestedStringField Tests
// =============================================================================

func TestReadNestedStringField_ValidNested(t *testing.T) {
	m := map[string]interface{}{
		"tokens": map[string]interface{}{
			"refresh_token": "nested-token",
			"access_token":  "nested-access",
		},
	}

	result := readNestedStringField(m, "tokens", "refresh_token", "refreshToken")
	if result != "nested-token" {
		t.Errorf("readNestedStringField = %q, want %q", result, "nested-token")
	}
}

func TestReadNestedStringField_CamelCaseFallback(t *testing.T) {
	m := map[string]interface{}{
		"tokens": map[string]interface{}{
			"refreshToken": "camel-token",
		},
	}

	result := readNestedStringField(m, "tokens", "refresh_token", "refreshToken")
	if result != "camel-token" {
		t.Errorf("readNestedStringField = %q, want %q", result, "camel-token")
	}
}

func TestReadNestedStringField_NilMap(t *testing.T) {
	result := readNestedStringField(nil, "tokens", "refresh_token")
	if result != "" {
		t.Errorf("readNestedStringField(nil) = %q, want empty", result)
	}
}

func TestReadNestedStringField_MissingNestedKey(t *testing.T) {
	m := map[string]interface{}{
		"other": map[string]interface{}{
			"refresh_token": "token",
		},
	}

	result := readNestedStringField(m, "tokens", "refresh_token")
	if result != "" {
		t.Errorf("readNestedStringField = %q, want empty (nested key missing)", result)
	}
}

func TestReadNestedStringField_NotAMap(t *testing.T) {
	m := map[string]interface{}{
		"tokens": "not a map",
	}

	result := readNestedStringField(m, "tokens", "refresh_token")
	if result != "" {
		t.Errorf("readNestedStringField = %q, want empty (not a map)", result)
	}
}

func TestReadNestedStringField_KeyNotString(t *testing.T) {
	m := map[string]interface{}{
		"tokens": map[string]interface{}{
			"refresh_token": 12345, // Not a string
		},
	}

	result := readNestedStringField(m, "tokens", "refresh_token")
	if result != "" {
		t.Errorf("readNestedStringField = %q, want empty (not a string)", result)
	}
}

func TestReadNestedStringField_EmptyValue(t *testing.T) {
	m := map[string]interface{}{
		"tokens": map[string]interface{}{
			"refresh_token": "",
		},
	}

	result := readNestedStringField(m, "tokens", "refresh_token")
	if result != "" {
		t.Errorf("readNestedStringField = %q, want empty", result)
	}
}

// =============================================================================
// readStringField Tests
// =============================================================================

func TestReadStringField_FirstKeyMatch(t *testing.T) {
	m := map[string]interface{}{
		"key1": "value1",
		"key2": "value2",
	}

	result := readStringField(m, "key1", "key2")
	if result != "value1" {
		t.Errorf("readStringField = %q, want %q", result, "value1")
	}
}

func TestReadStringField_SecondKeyMatch(t *testing.T) {
	m := map[string]interface{}{
		"key2": "value2",
	}

	result := readStringField(m, "key1", "key2")
	if result != "value2" {
		t.Errorf("readStringField = %q, want %q", result, "value2")
	}
}

func TestReadStringField_NilMap(t *testing.T) {
	result := readStringField(nil, "key1")
	if result != "" {
		t.Errorf("readStringField(nil) = %q, want empty", result)
	}
}

func TestReadStringField_NoMatch(t *testing.T) {
	m := map[string]interface{}{
		"other": "value",
	}

	result := readStringField(m, "key1", "key2")
	if result != "" {
		t.Errorf("readStringField = %q, want empty (no match)", result)
	}
}

func TestReadStringField_NotString(t *testing.T) {
	m := map[string]interface{}{
		"key1": 123,
	}

	result := readStringField(m, "key1")
	if result != "" {
		t.Errorf("readStringField = %q, want empty (not a string)", result)
	}
}

// =============================================================================
// getRefreshTokenFromJSON Nested Token Tests
// =============================================================================

func TestGetRefreshToken_NestedTokensField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	content := map[string]interface{}{
		"tokens": map[string]interface{}{
			"refresh_token": "nested-refresh-token",
		},
	}
	writeJSON(t, path, content)

	token, err := getRefreshTokenFromJSON(path)
	if err != nil {
		t.Fatalf("getRefreshTokenFromJSON error: %v", err)
	}
	if token != "nested-refresh-token" {
		t.Errorf("token = %q, want %q", token, "nested-refresh-token")
	}
}

func TestGetRefreshToken_ClaudeAiOauth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")

	content := map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			"refreshToken": "claude-oauth-refresh",
		},
	}
	writeJSON(t, path, content)

	token, err := getRefreshTokenFromJSON(path)
	if err != nil {
		t.Fatalf("getRefreshTokenFromJSON error: %v", err)
	}
	if token != "claude-oauth-refresh" {
		t.Errorf("token = %q, want %q", token, "claude-oauth-refresh")
	}
}

func TestGetRefreshToken_TopLevelPreferred(t *testing.T) {
	// When token exists at top level and nested, top level should be preferred
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	content := map[string]interface{}{
		"refresh_token": "top-level-token",
		"tokens": map[string]interface{}{
			"refresh_token": "nested-token",
		},
	}
	writeJSON(t, path, content)

	token, err := getRefreshTokenFromJSON(path)
	if err != nil {
		t.Fatalf("getRefreshTokenFromJSON error: %v", err)
	}
	if token != "top-level-token" {
		t.Errorf("token = %q, want %q (top level should be preferred)", token, "top-level-token")
	}
}

// =============================================================================
// snapshotMatchesProfile Tests
// =============================================================================

func TestSnapshotMatchesProfile(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(filepath.Join(tmpDir, "vault"))
	livePath := filepath.Join(tmpDir, "live", "auth.json")

	if err := os.MkdirAll(filepath.Dir(livePath), 0700); err != nil {
		t.Fatalf("failed to create live dir: %v", err)
	}

	snapshot := map[string][]byte{
		livePath: []byte(`{"access_token":"live"}`),
	}

	fileSet := authfile.AuthFileSet{
		Tool: "codex",
		Files: []authfile.AuthFileSpec{
			{Tool: "codex", Path: livePath},
		},
	}

	backupPath := vault.BackupPath("codex", "work", filepath.Base(livePath))
	if err := os.MkdirAll(filepath.Dir(backupPath), 0700); err != nil {
		t.Fatalf("failed to create backup dir: %v", err)
	}
	if err := os.WriteFile(backupPath, snapshot[livePath], 0600); err != nil {
		t.Fatalf("failed to write backup file: %v", err)
	}

	if !snapshotMatchesProfile(fileSet, vault, "work", snapshot) {
		t.Errorf("snapshotMatchesProfile() = false, want true")
	}
}

func TestSnapshotMatchesProfile_Mismatch(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(filepath.Join(tmpDir, "vault"))
	livePath := filepath.Join(tmpDir, "live", "auth.json")

	if err := os.MkdirAll(filepath.Dir(livePath), 0700); err != nil {
		t.Fatalf("failed to create live dir: %v", err)
	}

	snapshot := map[string][]byte{
		livePath: []byte(`{"access_token":"live"}`),
	}

	fileSet := authfile.AuthFileSet{
		Tool: "codex",
		Files: []authfile.AuthFileSpec{
			{Tool: "codex", Path: livePath},
		},
	}

	backupPath := vault.BackupPath("codex", "work", filepath.Base(livePath))
	if err := os.MkdirAll(filepath.Dir(backupPath), 0700); err != nil {
		t.Fatalf("failed to create backup dir: %v", err)
	}
	if err := os.WriteFile(backupPath, []byte(`{"access_token":"different"}`), 0600); err != nil {
		t.Fatalf("failed to write backup file: %v", err)
	}

	if snapshotMatchesProfile(fileSet, vault, "work", snapshot) {
		t.Errorf("snapshotMatchesProfile() = true, want false")
	}
}

// =============================================================================
// Helper Functions
// =============================================================================

func writeJSON(t *testing.T, path string, data interface{}) {
	t.Helper()
	jsonBytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal JSON: %v", err)
	}
	if err := os.WriteFile(path, jsonBytes, 0600); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
}

// NeedsRefresh is the one gate for spending a refresh token: only when the
// token has expired, or the provider just refused it. A token with time
// left is left alone — a refresh consumes the refresh token, and the
// families rotate.
func TestNeedsRefresh_OnlyWhenExpiredOrRefused(t *testing.T) {
	valid := &health.ProfileHealth{TokenExpiresAt: time.Now().Add(5 * time.Minute)}
	expired := &health.ProfileHealth{TokenExpiresAt: time.Now().Add(-time.Minute)}
	unknown := &health.ProfileHealth{}

	if NeedsRefresh(valid, nil) {
		t.Error("a token with five minutes left must not be refreshed")
	}
	if !NeedsRefresh(expired, nil) {
		t.Error("an expired token needs a refresh")
	}
	if !NeedsRefresh(valid, errors.New("unauthorized: status 401")) {
		t.Error("a token the provider just refused needs a refresh")
	}
	if NeedsRefresh(unknown, nil) || NeedsRefresh(nil, nil) {
		t.Error("with no expiry known and no refusal there is no reason to refresh")
	}
	if NeedsRefresh(valid, errors.New("API error: status 500")) {
		t.Error("a server error is not a refusal of the token")
	}
}
