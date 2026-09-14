package handoff

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// DefaultResumeDelay is the pause between ending a session and typing the
// resume command into the same pane: long enough for the tool to exit and
// the shell prompt to come back.
const DefaultResumeDelay = 1500 * time.Millisecond

// resumeArgs is the one table of the flags that reopen a tool's most
// recent session. The tool's binary is its provider id.
var resumeArgs = map[string][]string{
	"claude": {"--continue"},
	"codex":  {"resume", "--last"},
	"gemini": {"--resume", "latest"},
	"kimi":   {"--continue"},
}

// ResumeArgs returns the flags that reopen provider's most recent session,
// nil when caam does not know how to resume it. The slice is the caller's.
func ResumeArgs(provider string) []string {
	args, ok := resumeArgs[provider]
	if !ok {
		return nil
	}
	return append([]string(nil), args...)
}

// ResumeCommand is the shell command that reopens provider's most recent
// session ("claude --continue"), "" when caam does not know how.
func ResumeCommand(provider string) string {
	args, ok := resumeArgs[provider]
	if !ok {
		return ""
	}
	return provider + " " + strings.Join(args, " ")
}

// ResumeProviders lists, sorted, the providers whose sessions caam can
// resume.
func ResumeProviders() []string {
	providers := make([]string, 0, len(resumeArgs))
	for p := range resumeArgs {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	return providers
}

// ResumeInPane puts a switched account to use in a running session: it
// ends the session (/exit), waits delay for the shell to come back, then
// types the resume command. send types one line into the pane. A
// cancelled ctx during the wait returns ctx.Err() and types nothing more.
func ResumeInPane(ctx context.Context, send func(text string) error, resume string, delay time.Duration) error {
	if err := send("/exit\n"); err != nil {
		return fmt.Errorf("end the session: %w", err)
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := send(resume + "\n"); err != nil {
		return fmt.Errorf("resume the session: %w", err)
	}
	return nil
}
