package keychain_test

// CAAM_DEBUG diagnostics for the bridge (work item B of the switcher handoff).

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
)

const sampleDebugBlob = `{"claudeAiOauth":{"accessToken":"at-live","refreshToken":"rt-live","expiresAt":1893456000000}}`

// TestDebugDiagnosticsNameTheLookupNotTheSecret: CAAM_DEBUG=1 must show
// which item was looked up and how `security` answered, so a bridge that
// quietly reports "no item" can be told apart from one that was denied or
// never ran — without ever writing the secret to the log.
func TestDebugDiagnosticsNameTheLookupNotTheSecret(t *testing.T) {
	items := testutil.FakeKeychain(t)
	t.Setenv("CAAM_DEBUG", "1")

	var log bytes.Buffer
	prev := keychain.DebugOutput
	keychain.DebugOutput = &log
	t.Cleanup(func() { keychain.DebugOutput = prev })

	// A miss first: exit 44 and the not-found diagnostic must be visible.
	if _, err := keychain.ReadClaude(); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("keychain.ReadClaude() with no item = %v, want ErrNotFound", err)
	}
	out := log.String()
	for _, want := range []string{"find-generic-password", keychain.ClaudeService, "exit=44", "could not be found"} {
		if !strings.Contains(out, want) {
			t.Errorf("debug log lacks %q:\n%s", want, out)
		}
	}

	// Then a hit: the read is reported by size only.
	log.Reset()
	testutil.FakeKeychainStore(t, items, keychain.ClaudeService, keychain.LoginAccount(), sampleDebugBlob)
	if _, err := keychain.ReadClaude(); err != nil {
		t.Fatalf("keychain.ReadClaude(): %v", err)
	}
	out = log.String()
	if !strings.Contains(out, "exit=0") || !strings.Contains(out, "Claude item read") {
		t.Errorf("debug log does not report the successful read:\n%s", out)
	}
	if strings.Contains(out, "at-live") || strings.Contains(out, sampleDebugBlob) {
		t.Fatalf("debug log leaked the secret:\n%s", out)
	}
}
