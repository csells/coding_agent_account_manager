package cmd

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
)

// TestWhich_KnowsEveryProvider: `caam which` reports the default account of
// every tool caam manages — a default set on Antigravity or Kimi used to be
// invisible because the command knew three providers — in the strip's
// order, each named by its product name.
func TestWhich_KnowsEveryProvider(t *testing.T) {
	oldCfg := cfg
	cfg = config.DefaultConfig()
	t.Cleanup(func() { cfg = oldCfg })
	cfg.SetDefault("agy", "goog")
	cfg.SetDefault("kimi", "k1")

	out, err := captureStdout(t, func() error { return whichCmd.RunE(whichCmd, nil) })
	if err != nil {
		t.Fatalf("which: %v", err)
	}

	for _, want := range []string{"Antigravity: goog", "Kimi Code: k1", "Codex: (none)"} {
		if !strings.Contains(out, want) {
			t.Errorf("which lacks %q:\n%s", want, out)
		}
	}
	for id := range tools {
		if !strings.Contains(out, provider.Label(id)+":") {
			t.Errorf("which does not list %s:\n%s", provider.Label(id), out)
		}
	}
	if strings.Contains(out, "agy:") || strings.Contains(out, "No defaults set") {
		t.Errorf("which uses a raw id or denies the defaults it has:\n%s", out)
	}
	// Strip order: Claude Code first, then Codex, with Antigravity before Kimi.
	if strings.Index(out, "Claude Code:") > strings.Index(out, "Codex:") || strings.Index(out, "Antigravity:") > strings.Index(out, "Kimi Code:") {
		t.Errorf("which lists tools out of strip order:\n%s", out)
	}

	// A single provider still works, by id, and is named by its label.
	out, err = captureStdout(t, func() error { return whichCmd.RunE(whichCmd, []string{"agy"}) })
	if err != nil {
		t.Fatalf("which agy: %v", err)
	}
	if strings.TrimSpace(out) != "Antigravity: goog" {
		t.Errorf("which agy = %q, want %q", strings.TrimSpace(out), "Antigravity: goog")
	}
}
