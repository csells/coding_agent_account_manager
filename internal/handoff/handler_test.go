package handoff

import (
	"reflect"
	"testing"
)

func TestNewRegistry(t *testing.T) {
	r := NewRegistry()
	if r == nil {
		t.Fatal("NewRegistry() returned nil")
	}

	// Should have default handlers registered
	providers := r.Providers()
	if len(providers) < 3 {
		t.Errorf("expected at least 3 providers, got %d", len(providers))
	}

	t.Logf("[TEST] Registered providers: %v", providers)
}

func TestRegistry_Get(t *testing.T) {
	r := NewRegistry()

	tests := []struct {
		provider string
		wantNil  bool
	}{
		{"claude", false},
		{"codex", false},
		{"gemini", false},
		{"unknown", true},
	}

	for _, tc := range tests {
		t.Run(tc.provider, func(t *testing.T) {
			h := r.Get(tc.provider)
			if tc.wantNil && h != nil {
				t.Errorf("Get(%q) = %v, want nil", tc.provider, h)
			}
			if !tc.wantNil && h == nil {
				t.Errorf("Get(%q) = nil, want non-nil", tc.provider)
			}
		})
	}
}

func TestGetHandler(t *testing.T) {
	// Test default registry access
	h := GetHandler("claude")
	if h == nil {
		t.Error("GetHandler(claude) returned nil")
	}

	if h.Provider() != "claude" {
		t.Errorf("Provider() = %q, want claude", h.Provider())
	}
}

// Each handler names its provider and the flags that reopen the tool's
// most recent session after a switch.
func TestHandlers_ProviderAndResumeArgs(t *testing.T) {
	tests := []struct {
		handler    LoginHandler
		provider   string
		resumeArgs []string
	}{
		{&ClaudeLoginHandler{}, "claude", []string{"--continue"}},
		{&CodexLoginHandler{}, "codex", []string{"resume", "--last"}},
		{&GeminiLoginHandler{}, "gemini", []string{"--resume", "latest"}},
	}
	for _, tc := range tests {
		t.Run(tc.provider, func(t *testing.T) {
			if got := tc.handler.Provider(); got != tc.provider {
				t.Errorf("Provider() = %q, want %q", got, tc.provider)
			}
			if got := tc.handler.ResumeArgs(); !reflect.DeepEqual(got, tc.resumeArgs) {
				t.Errorf("ResumeArgs() = %v, want %v", got, tc.resumeArgs)
			}
		})
	}
}

// TestRegistry_Register tests custom handler registration.
func TestRegistry_Register(t *testing.T) {
	r := NewRegistry()

	// Create a custom handler for testing
	custom := &ClaudeLoginHandler{} // Reuse for simplicity

	// Override claude handler
	r.Register(custom)

	got := r.Get("claude")
	if got != custom {
		t.Error("custom handler not registered correctly")
	}
}
