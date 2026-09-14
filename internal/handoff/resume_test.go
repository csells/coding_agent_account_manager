package handoff

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// One table says how each tool's last session is reopened; the flags and
// the shell command agree, and an unknown tool has neither.
func TestResumeTable(t *testing.T) {
	want := map[string]string{
		"claude": "claude --continue",
		"codex":  "codex resume --last",
		"gemini": "gemini --resume latest",
		"kimi":   "kimi --continue",
	}
	if got := ResumeProviders(); !reflect.DeepEqual(got, []string{"claude", "codex", "gemini", "kimi"}) {
		t.Fatalf("ResumeProviders() = %v", got)
	}
	for provider, command := range want {
		if got := ResumeCommand(provider); got != command {
			t.Errorf("ResumeCommand(%q) = %q, want %q", provider, got, command)
		}
		args := ResumeArgs(provider)
		if got := provider + " " + join(args); got != command {
			t.Errorf("ResumeArgs(%q) = %v, does not spell %q", provider, args, command)
		}
		args[0] = "changed"
		if ResumeArgs(provider)[0] == "changed" {
			t.Errorf("ResumeArgs(%q) hands out the table's own slice", provider)
		}
	}
	if ResumeArgs("aider") != nil || ResumeCommand("aider") != "" {
		t.Error("an unknown tool must have no resume flags or command")
	}
}

func join(args []string) string {
	s := ""
	for i, a := range args {
		if i > 0 {
			s += " "
		}
		s += a
	}
	return s
}

// A pane is resumed by ending its session, waiting, then typing the resume
// command; a failed send stops there and says which step failed.
func TestResumeInPane(t *testing.T) {
	var sent []string
	send := func(text string) error { sent = append(sent, text); return nil }
	if err := ResumeInPane(context.Background(), send, "claude --continue", 0); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sent, []string{"/exit\n", "claude --continue\n"}) {
		t.Fatalf("sent %q", sent)
	}

	boom := errors.New("pane gone")
	sent = nil
	failing := func(text string) error { sent = append(sent, text); return boom }
	err := ResumeInPane(context.Background(), failing, "claude --continue", 0)
	if !errors.Is(err, boom) || len(sent) != 1 {
		t.Fatalf("a failed /exit must stop the resume: err=%v sent=%q", err, sent)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sent = nil
	err = ResumeInPane(ctx, send, "claude --continue", DefaultResumeDelay)
	if !errors.Is(err, context.Canceled) || len(sent) != 1 {
		t.Fatalf("a cancelled wait must type nothing more: err=%v sent=%q", err, sent)
	}
}
